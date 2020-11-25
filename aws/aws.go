package aws

import (
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/pkg/errors"
)

var ecsClient *ecs.ECS
var ec2ICClient *ec2instanceconnect.EC2InstanceConnect
var ec2Client *ec2.EC2

func init() {
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		// Load the user's config from the env
		// This way it will automatically use the profile the user has configured
		SharedConfigState: session.SharedConfigEnable,
	}))
	ecsClient = ecs.New(sess)
	ec2ICClient = ec2instanceconnect.New(sess)
	ec2Client = ec2.New(sess)
}

// ListServices returns the names of all available ECS services.
func ListServices() ([]string, error) {
	// This is paginated but if we ever have that many clusters that
	// it's an issue then we have a bigger problem
	respListClusters, err := ecsClient.ListClusters(&ecs.ListClustersInput{
		MaxResults: aws.Int64(100),
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list available ECS Clusters")
	}

	var serviceNames []string
	for _, clusterARN := range respListClusters.ClusterArns {
		respListServices, err := ecsClient.ListServices(&ecs.ListServicesInput{
			Cluster:    clusterARN,
			MaxResults: aws.Int64(100),
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list services in cluster %s", *clusterARN)
		}

		for _, arn := range respListServices.ServiceArns {
			// TODO(@cszatmary): Use a regex to parse the ARN
			parts := strings.Split(*arn, "/")
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
	var clusterARNs []*string
	if opts.ClusterName != "" {
		respDescribeClusters, err := ecsClient.DescribeClusters(&ecs.DescribeClustersInput{
			Clusters: []*string{aws.String(opts.ClusterName)},
		})
		if err != nil {
			return ServiceContainer{}, errors.Wrapf(err, "failed to find cluster %s", opts.ClusterName)
		}

		if len(respDescribeClusters.Clusters) == 0 {
			return ServiceContainer{}, errors.Errorf("no cluster named %s found", opts.ClusterName)
		}

		clusterARNs = []*string{respDescribeClusters.Clusters[0].ClusterArn}
	} else {
		respListClusters, err := ecsClient.ListClusters(&ecs.ListClustersInput{
			MaxResults: aws.Int64(100),
		})
		if err != nil {
			return ServiceContainer{}, errors.Wrap(err, "failed to list available ECS clusters")
		}

		clusterARNs = respListClusters.ClusterArns
	}

	// Find which cluster our service is on and some currently running task IDs
	desiredStatus := "RUNNING"
	if opts.FindLogs {
		desiredStatus = "STOPPED"
	}

	var clusterARN *string
	var taskArns []*string
	for _, arn := range clusterARNs {
		respListTasks, err := ecsClient.ListTasks(&ecs.ListTasksInput{
			Cluster:       arn,
			ServiceName:   aws.String(opts.ServiceName),
			DesiredStatus: aws.String(desiredStatus),
			MaxResults:    aws.Int64(100),
		})
		if err != nil {
			// Ignore if not found since we are checking multiple clusters
			aerr, ok := err.(awserr.Error)
			if ok && aerr.Code() == ecs.ErrCodeServiceNotFoundException {
				continue
			}

			return ServiceContainer{}, errors.Wrapf(err, "failed to list tasks for %s in cluster %s", opts.ServiceName, *arn)
		}

		// Stop after finding a match
		clusterARN = arn
		taskArns = respListTasks.TaskArns
		break
	}

	if len(taskArns) == 0 {
		if opts.FindLogs {
			return ServiceContainer{}, errors.Errorf("failed to find stopped tasks for %s", opts.ServiceName)
		}

		return ServiceContainer{}, errors.Errorf("failed to find tasks for %s", opts.ServiceName)
	}

	// Find a container instance ARN from the first running task we found
	respDescribeTasks, err := ecsClient.DescribeTasks(&ecs.DescribeTasksInput{
		Cluster: clusterARN,
		Tasks:   taskArns,
	})
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to describe tasks for %s", opts.ServiceName)
	}

	// Just use the first one
	task := respDescribeTasks.Tasks[0]
	// TODO(@cszatmary): Use a regex to parse the ARN
	parts := strings.Split(*task.TaskDefinitionArn, "/")
	taskDefName := parts[len(parts)-1]
	// TODO(@cszatmary): Wtf is going on here?
	taskDefName = strings.Replace(taskDefName, ":", "-", -1)

	// Find the EC2 instance ID of the instance our container is running on
	respDescribeContainerInstances, err := ecsClient.DescribeContainerInstances(&ecs.DescribeContainerInstancesInput{
		Cluster:            clusterARN,
		ContainerInstances: []*string{task.ContainerInstanceArn},
	})
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to describe container instance for %s", opts.ServiceName)
	}

	if len(respDescribeContainerInstances.ContainerInstances) == 0 {
		return ServiceContainer{}, errors.Errorf("no container instances found for %s", opts.ServiceName)
	}

	// Find the private IP of our instance
	instanceID := respDescribeContainerInstances.ContainerInstances[0].Ec2InstanceId
	respDescribeInstances, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
		InstanceIds: []*string{instanceID},
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
	instance := respDescribeInstances.Reservations[0].Instances[0]
	respSendSSHPublicKey, err := ec2ICClient.SendSSHPublicKey(&ec2instanceconnect.SendSSHPublicKeyInput{
		AvailabilityZone: instance.Placement.AvailabilityZone,
		InstanceId:       instanceID,
		InstanceOSUser:   aws.String("ec2-user"),
		SSHPublicKey:     aws.String(string(sshPublicKey)),
	})
	if err != nil {
		return ServiceContainer{}, errors.Wrapf(err, "failed to send SSH key to EC2 instance %s", *instanceID)
	}

	// Triple state failures are the best
	if respSendSSHPublicKey.Success == nil || !*respSendSSHPublicKey.Success {
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
