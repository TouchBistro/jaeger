# Jaeger

Jaeger is a tool to aid in live debugging of ECS containers. It makes it easy to ssh into a container and spawn a shell.

## How it works

Jaeger uses [EC2 Instance Connect](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/Connect-using-EC2-Instance-Connect.html) for connecting to EC2 instances and ECS containers.
It will push your public ssh key to EC2 which will then allow you to authenticate.

Jaeger will take a service name, discover which cluster it exists on, discover which instances it has placed tasks on,
fetch a container ID, and exec SSH and spawn a bash shell on that container.

## Usage

```
jaeger ssh <service>
```

Run `jaeger -h` to see available commands. You can view help for a given command by running `jaeger <command> -h`.
