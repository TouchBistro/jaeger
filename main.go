package main

import (
	"flag"
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
	if err != nil {
		log.Panic(err)
	}

	//Find which cluster our service is on and some currently running task IDs
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
	if len(taskArns) < 1 {
		log.Fatal("Could not find service")
	}

	//Find a container instance ARN from the first running task we found
	taskInput := &ecs.DescribeTasksInput{
		Tasks:   taskArns,
		Cluster: clusterArn,
	}
	taskResults, err := svcEcs.DescribeTasks(taskInput)
	if err != nil {
		log.Panic(err)
	}
	containerArn := taskResults.Tasks[0].ContainerInstanceArn

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
		log.Panic(err)
	}
	instanceIp := instanceResults.Reservations[0].Instances[0].NetworkInterfaces[0].PrivateIpAddress

	//SSH to the EC2 instance to `docker ps` and find our container id
	sshString := string("ec2-user@" + *instanceIp)
	grepString := "docker ps | grep " + *service + " | head -n1 | tr '\\t' ' ' | cut -d ' ' -f 1"
	containerIdCmd := exec.Command("ssh", sshString, grepString)
	containerIdResult, err := containerIdCmd.Output()
	if err != nil {
		log.Panic(err)
	}
	containerId := string(containerIdResult)
	containerId = strings.TrimSuffix(containerId, "\n")

	//Exec into ssh, passing the container id to drop right into shell
	runCmd := string("docker exec -ti " + containerId + " sh")
	sshPath, lookErr := exec.LookPath("ssh")
	if lookErr != nil {
		log.Panic(lookErr)
	}
	sshArgs := []string{"ssh", "-t", sshString, runCmd}
	env := os.Environ()
	log.Println(sshArgs)
	execErr := syscall.Exec(sshPath, sshArgs, env)
	if execErr != nil {
		log.Panic(execErr)
	}
}
