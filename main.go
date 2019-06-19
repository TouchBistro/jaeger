package main

import (
	"flag"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func handleAwsErr(err error) {
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case ecs.ErrCodeServerException:
				log.Println(ecs.ErrCodeServerException, aerr.Error())
			case ecs.ErrCodeClientException:
				log.Println(ecs.ErrCodeClientException, aerr.Error())
			case ecs.ErrCodeInvalidParameterException:
				log.Println(ecs.ErrCodeInvalidParameterException, aerr.Error())
			case ecs.ErrCodeClusterNotFoundException:
				log.Println(ecs.ErrCodeClusterNotFoundException, aerr.Error())
			case ecs.ErrCodeServiceNotFoundException:
				log.Println(ecs.ErrCodeServiceNotFoundException, aerr.Error())
			case ecs.ErrCodeServiceNotActiveException:
				log.Println(ecs.ErrCodeServiceNotActiveException, aerr.Error())
			case ecs.ErrCodePlatformUnknownException:
				log.Println(ecs.ErrCodePlatformUnknownException, aerr.Error())
			case ecs.ErrCodePlatformTaskDefinitionIncompatibilityException:
				log.Println(ecs.ErrCodePlatformTaskDefinitionIncompatibilityException, aerr.Error())
			case ecs.ErrCodeAccessDeniedException:
				log.Println(ecs.ErrCodeAccessDeniedException, aerr.Error())
			default:
				log.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			log.Println(err.Error())
		}
		return
	}
}

func main() {
	service := flag.String("service", "", "Service on ECS to hunt for a container of")
	flag.Parse()

	if *service == "" {
		log.Fatal("Unset flags, need -service")
	}

	//Connect to ECS API
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
	svcEcs := ecs.New(sess)

	listInput := &ecs.ListClustersInput{}

	listResults, err := svcEcs.ListClusters(listInput)
	handleAwsErr(err)

	var clusterArn *string
	var taskArns []*string

	for _, arn := range listResults.ClusterArns {
		input := &ecs.ListTasksInput{
			Cluster:     arn,
			ServiceName: service,
			MaxResults:  aws.Int64(100),
		}
		result, err := svcEcs.ListTasks(input)
		if err == nil {
			taskArns = result.TaskArns
			clusterArn = arn
		}
	}
	if len(taskArns) > 0 {
		//log.Println(*taskArns[0])
	} else {
		log.Fatal("Could not find service")
	}

	taskInput := &ecs.DescribeTasksInput{
		Tasks:   taskArns,
		Cluster: clusterArn,
	}
	taskResults, err := svcEcs.DescribeTasks(taskInput)
	//log.Println(taskResults)
	handleAwsErr(err)
	containerArn := taskResults.Tasks[0].ContainerInstanceArn
	containerInput := &ecs.DescribeContainerInstancesInput{
		ContainerInstances: []*string{containerArn},
		Cluster:            clusterArn,
	}
	containerResults, err := svcEcs.DescribeContainerInstances(containerInput)
	instanceId := containerResults.ContainerInstances[0].Ec2InstanceId

	svcEc2 := ec2.New(sess)
	instanceInput := &ec2.DescribeInstancesInput{
		InstanceIds: []*string{instanceId},
	}
	instanceResults, err := svcEc2.DescribeInstances(instanceInput)
	//log.Println(instanceResults)
	handleAwsErr(err)

	instanceIp := instanceResults.Reservations[0].Instances[0].NetworkInterfaces[0].PrivateIpAddress
	//log.Println(*instanceIp)

	sshString := string("ec2-user@" + *instanceIp)
	grepString := "docker ps | grep " + *service + " | tr '\\t' ' ' | cut -d ' ' -f 1"
	containerIdCmd := exec.Command("ssh", sshString, grepString)

	containerIdResult, err := containerIdCmd.Output()
	if err != nil {
		log.Panic(err)
	}
	containerId := string(containerIdResult)
	containerId = strings.TrimSuffix(containerId, "\n")

	//log.Println(containerId)

	runCmd := string("docker exec -ti " + containerId + " sh")

	sshPath, lookErr := exec.LookPath("ssh")
	if lookErr != nil {
		log.Panic(lookErr)
	}

	sshArgs := []string{"ssh", "-t", sshString, runCmd}

	env := os.Environ()

	execErr := syscall.Exec(sshPath, sshArgs, env)
	if execErr != nil {
		log.Panic(execErr)
	}
}
