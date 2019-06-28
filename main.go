package main

import (
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func main() {
	//Process flag (Maybe might make this a single arg)
	logs := flag.Bool("logs", false, "Check for log output from dead containers instead")
	flag.Parse()

	var service *string
	if len(os.Args) > 1 {
		service = &os.Args[len(os.Args)-1]
	} else {
		blank := ""
		service = &blank
	}

	//Connect to ECS API
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
	svcEcs := ecs.New(sess)

	//List existing clusters to check
	listInput := &ecs.ListClustersInput{}
	listResults, err := svcEcs.ListClusters(listInput)
	if err != nil {
		log.Fatal(err)
	}

	if *service == "" {
		for _, arn := range listResults.ClusterArns {
			input := &ecs.ListServicesInput{
				Cluster:    arn,
				MaxResults: aws.Int64(100),
			}
			result, err := svcEcs.ListServices(input)
			if err == nil {
				for _, arn := range result.ServiceArns {
					t := strings.Split(*arn, "/")
					fmt.Println(t[len(t)-1])
				}
			}
		}
		os.Exit(0)
	}

	//Find which cluster our service is on and some currently running task IDs
	var clusterArn *string
	var taskArns []*string
	for _, arn := range listResults.ClusterArns {
		desiredStatus := "RUNNING"
		if *logs {
			desiredStatus = "STOPPED"
		}
		input := &ecs.ListTasksInput{
			Cluster:       arn,
			ServiceName:   service,
			DesiredStatus: &desiredStatus,
			MaxResults:    aws.Int64(100),
		}
		result, err := svcEcs.ListTasks(input)
		if err == nil {
			taskArns = result.TaskArns
			clusterArn = arn
		}
	}
	if len(taskArns) < 1 {
		if *logs {
			log.Fatal("Could not find any stopped tasks for " + *service)
		} else {
			log.Fatal("Could not find service")
		}
	}

	//Find a container instance ARN from the first running task we found
	taskInput := &ecs.DescribeTasksInput{
		Tasks:   taskArns,
		Cluster: clusterArn,
	}
	taskResults, err := svcEcs.DescribeTasks(taskInput)
	if err != nil {
		log.Fatal(err)
	}
	containerArn := taskResults.Tasks[0].ContainerInstanceArn
	taskArn := taskResults.Tasks[0].TaskDefinitionArn
	t := strings.Split(*taskArn, "/")
	taskDefName := t[len(t)-1]
	taskDefName = strings.Replace(taskDefName, ":", "-", -1)

	//Find the EC2 instance ID of the instance our container is running on
	containerInput := &ecs.DescribeContainerInstancesInput{
		ContainerInstances: []*string{containerArn},
		Cluster:            clusterArn,
	}
	containerResults, err := svcEcs.DescribeContainerInstances(containerInput)
	instanceId := containerResults.ContainerInstances[0].Ec2InstanceId

	//Find the private IP of our instance
	svcEc2 := ec2.New(sess)
	instanceInput := &ec2.DescribeInstancesInput{
		InstanceIds: []*string{instanceId},
	}
	instanceResults, err := svcEc2.DescribeInstances(instanceInput)
	if err != nil {
		log.Fatal(err)
	}
	instanceIp := instanceResults.Reservations[0].Instances[0].NetworkInterfaces[0].PrivateIpAddress

	//SSH to the EC2 instance to `docker ps` and find our container id
	sshString := string("ec2-user@" + *instanceIp)
	grepString := "docker ps | grep " + taskDefName + " | head -n1 | tr '\\t' ' ' | cut -d ' ' -f 1"
	if *logs {
		grepString = "docker ps -a | grep " + taskDefName + " | head -n1 | tr '\\t' ' ' | cut -d ' ' -f 1"
	}
	containerIdCmd := exec.Command("ssh", sshString, grepString)
	containerIdCmd.Stderr = os.Stderr
	containerIdResult, err := containerIdCmd.Output()
	if err != nil {
		log.Fatalf("Failed to retrieve container id: %s\n", err)
	}
	containerId := string(containerIdResult)
	containerId = strings.TrimSuffix(containerId, "\n")

	//Exec into ssh, passing the container id to drop right into shell
	runCmd := string("docker exec -ti " + containerId + " sh")
	if *logs {
		runCmd = string("docker logs " + containerId)
	}
	sshPath, lookErr := exec.LookPath("ssh")
	if lookErr != nil {
		log.Fatalf("Could not find ssh: %s\n", lookErr)
		log.Fatal(lookErr)
	}
	sshArgs := []string{"ssh", "-t", sshString, runCmd}
	env := os.Environ()
	fmt.Println(sshArgs)
	execErr := syscall.Exec(sshPath, sshArgs, env)
	if execErr != nil {
		log.Fatalf("Failed to exec ssh: %s\n", execErr)
	}
}
