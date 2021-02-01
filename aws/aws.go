package aws

import (
	"context"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/TouchBistro/goutils/fatal"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/pkg/errors"
)

var awsConfig aws.Config

func init() {
	var err error
	awsConfig, err = config.LoadDefaultConfig(context.Background())
	if err != nil {
		fatal.ExitErr(err, "Failed to initialize AWS config")
	}
}

// ListServices returns the names of all available ECS services.
func ListServices() ([]string, error) {
	ecsClient := ecs.NewFromConfig(awsConfig)
	ctx := context.Background()

	// This is paginated but if we ever have that many clusters that
	// it's an issue then we have a bigger problem
	listClustersOutput, err := ecsClient.ListClusters(ctx, &ecs.ListClustersInput{
		MaxResults: aws.Int32(100),
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list available ECS Clusters")
	}

	var serviceNames []string
	for _, clusterARN := range listClustersOutput.ClusterArns {
		listServicesOutput, err := ecsClient.ListServices(ctx, &ecs.ListServicesInput{
			Cluster:    aws.String(clusterARN),
			MaxResults: aws.Int32(100),
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list services in cluster %s", clusterARN)
		}

		for _, arn := range listServicesOutput.ServiceArns {
			// TODO(@cszatmary): Use a regex to parse the ARN
			parts := strings.Split(arn, "/")
			serviceNames = append(serviceNames, parts[len(parts)-1])
		}
	}

	return serviceNames, nil
}

type FindServiceContainerOptions struct {
	ServiceName      string
	ClusterName      string
	FindLogs         bool
	SSHPublicKeyPath string
}

type ServiceContainer struct {
	InstanceDNSName string
	ContainerID     string
}

func FindServiceContainer(opts FindServiceContainerOptions) (ServiceContainer, error) {
	ecsClient := ecs.NewFromConfig(awsConfig)
	ctx := context.Background()
	var clusterARNs []string
	if opts.ClusterName != "" {
		describeClustersOutput, err := ecsClient.DescribeClusters(ctx, &ecs.DescribeClustersInput{
			Clusters: []string{opts.ClusterName},
		})
		if err != nil {
			return ServiceContainer{}, errors.Wrapf(err, "failed to find cluster %s", opts.ClusterName)
		}

		if len(describeClustersOutput.Clusters) == 0 {
			return ServiceContainer{}, errors.Errorf("no cluster named %s found", opts.ClusterName)
		}

		clusterARNs = []string{*describeClustersOutput.Clusters[0].ClusterArn}
	} else {
		listClustersOutput, err := ecsClient.ListClusters(ctx, &ecs.ListClustersInput{
			MaxResults: aws.Int32(100),
		})
		if err != nil {
			return ServiceContainer{}, errors.Wrap(err, "failed to list available ECS clusters")
		}

		clusterARNs = listClustersOutput.ClusterArns
	}

	// Find which cluster our service is on and some currently running task IDs
	desiredStatus := ecstypes.DesiredStatusRunning
	if opts.FindLogs {
		desiredStatus = ecstypes.DesiredStatusStopped
	}

	var clusterARN string
	var taskArns []string
	for _, arn := range clusterARNs {
		listTasksOutput, err := ecsClient.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:       aws.String(arn),
			ServiceName:   aws.String(opts.ServiceName),
			DesiredStatus: desiredStatus,
			MaxResults:    aws.Int32(100),
		})
		if err != nil {
			// Ignore if not found since we are checking multiple clusters
			var notFoundErr *ecstypes.ResourceNotFoundException
			if errors.As(err, &notFoundErr) {
				continue
			}
			return ServiceContainer{}, errors.Wrapf(err, "failed to list tasks for %s in cluster %s", opts.ServiceName, arn)
		}

		// Stop after finding a match
		clusterARN = arn
		taskArns = listTasksOutput.TaskArns
		break
	}

	if len(taskArns) == 0 {
		if opts.FindLogs {
			return ServiceContainer{}, errors.Errorf("failed to find stopped tasks for %s", opts.ServiceName)
		}

		return ServiceContainer{}, errors.Errorf("failed to find tasks for %s", opts.ServiceName)
	}

	// Find a container instance ARN from the first running task we found
	describeTasksOutput, err := ecsClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterARN),
		Tasks:   taskArns,
	})
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to describe tasks for %s", opts.ServiceName)
	}

	// Just use the first one
	task := describeTasksOutput.Tasks[0]
	// TODO(@cszatmary): Use a regex to parse the ARN
	parts := strings.Split(*task.TaskDefinitionArn, "/")
	taskDefName := parts[len(parts)-1]
	// TODO(@cszatmary): Wtf is going on here?
	taskDefName = strings.Replace(taskDefName, ":", "-", -1)

	// Find the EC2 instance ID of the instance our container is running on
	describeContainerInstancesOutput, err := ecsClient.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
		Cluster:            aws.String(clusterARN),
		ContainerInstances: []string{*task.ContainerInstanceArn},
	})
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to describe container instance for %s", opts.ServiceName)
	}

	if len(describeContainerInstancesOutput.ContainerInstances) == 0 {
		return ServiceContainer{}, errors.Errorf("no container instances found for %s", opts.ServiceName)
	}

	// Find the private IP of our instance
	instanceID := describeContainerInstancesOutput.ContainerInstances[0].Ec2InstanceId
	ec2Client := ec2.NewFromConfig(awsConfig)
	describeInstancesOutput, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{*instanceID},
	})
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to find EC2 instance for %s", opts.ServiceName)
	}

	// Get the absolute path to the ssh public key that will be used
	sshPublicKeyPath := opts.SSHPublicKeyPath
	if sshPublicKeyPath == "" {
		sshPublicKeyPath = "~/.ssh/id_rsa.pub"
	}

	// Path can be prefixed with ~ for convenience
	if strings.HasPrefix(sshPublicKeyPath, "~") {
		sshPublicKeyPath = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(sshPublicKeyPath, "~"))
	} else if !filepath.IsAbs(sshPublicKeyPath) {
		absPath, err := filepath.Abs(sshPublicKeyPath)
		if err != nil {
			return ServiceContainer{}, errors.Wrapf(err, "failed to resolve absolute path to %s", sshPublicKeyPath)
		}
		sshPublicKeyPath = absPath
	}

	sshPublicKey, err := ioutil.ReadFile(sshPublicKeyPath)
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to read file %s", sshPublicKeyPath)
	}

	// TODO(@cszatmary): This is kinda sketchy. This is a great place for an out of bounds panic
	instance := describeInstancesOutput.Reservations[0].Instances[0]
	ec2ICClient := ec2instanceconnect.NewFromConfig(awsConfig)
	sendSSHPublicKeyOutput, err := ec2ICClient.SendSSHPublicKey(ctx, &ec2instanceconnect.SendSSHPublicKeyInput{
		AvailabilityZone: instance.Placement.AvailabilityZone,
		InstanceId:       instanceID,
		InstanceOSUser:   aws.String("ec2-user"),
		SSHPublicKey:     aws.String(string(sshPublicKey)),
	})
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to send SSH key to EC2 instance %s", *instanceID)
	}
	if !sendSSHPublicKeyOutput.Success {
		return ServiceContainer{}, errors.Errorf("failed to send ssh key to EC2 instance %s", *instanceID)
	}

	// SSH to the EC2 instance to `docker ps` and find our container id
	instanceDNSName := *instance.PrivateDnsName
	sshArgs := []string{"ec2-user@" + instanceDNSName}
	if opts.FindLogs {
		// Want to list exited containers too so we can grab the logs
		sshArgs = append(sshArgs, "docker ps -a")
	} else {
		sshArgs = append(sshArgs, "docker ps")
	}

	// Filter only ID and name which is what we care about
	sshArgs = append(sshArgs, "--format", "'{{.ID}} {{.Names}}'")

	dockerPSCmd := exec.Command("ssh", sshArgs...)
	dockerPSCmd.Stderr = os.Stderr
	dockerPSOutput, err := dockerPSCmd.Output()
	if err != nil {
		return ServiceContainer{}, errors.Wrap(err, "failed to retrieve container id")
	}

	containers := strings.Split(string(dockerPSOutput), "\n")
	// There's a trailing newline so drop last element as it will be an empty string
	containers = containers[:len(containers)-1]
	var containerID string
	for _, c := range containers {
		parts := strings.Split(c, " ")
		name := parts[1]
		if strings.Contains(name, taskDefName) {
			containerID = parts[0]
			break
		}
	}

	if containerID == "" {
		return ServiceContainer{}, errors.Errorf("failed to find container ID for %s", opts.ServiceName)
	}

	return ServiceContainer{
		InstanceDNSName: instanceDNSName,
		ContainerID:     containerID,
	}, nil
}
