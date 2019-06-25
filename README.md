# Jaeger
Jaeger is a tool to aid in live debugging of ECS containers. It assumes you've used `ssh-add` to add the key needed for any container instaces to your ssh agent, and will then take a service name, discover which cluster it exists on, discover which instances it has placed tasks on, fetch a container ID, and exec SSH and spawn a `sh` on that container.

```
Usage of ./jaeger:
  ./jaeger
    	List all services available across every visible cluster
  ./jaeger service-name
    	Attempt to resolve container location and ssh directly into a sh on that container
```
