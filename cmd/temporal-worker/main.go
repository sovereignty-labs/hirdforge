package main

import (
	"flag"
	"log"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/kitporath/project_valhalla/pkg/workflows"
)

func main() {
	temporalAddr := flag.String("temporal-addr", getEnvOrDefault("TEMPORAL_ADDR", "temporal.valhalla.svc.cluster.local:7233"), "Temporal server address")
	namespace := flag.String("namespace", getEnvOrDefault("TEMPORAL_NAMESPACE", "default"), "Temporal namespace")
	flag.Parse()

	log.Printf("Connecting to Temporal at %s (namespace: %s)", *temporalAddr, *namespace)

	c, err := client.Dial(client.Options{
		HostPort:  *temporalAddr,
		Namespace: *namespace,
	})
	if err != nil {
		log.Fatalf("Failed to create Temporal client: %v", err)
	}
	defer c.Close()

	w := worker.New(c, workflows.TaskQueue, worker.Options{})

	activities := &workflows.AgentActivities{}
	w.RegisterWorkflow(workflows.SovereignSessionWorkflow)
	w.RegisterActivity(activities)

	log.Printf("Starting Temporal worker on task queue: %s", workflows.TaskQueue)

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalf("Worker exited with error: %v", err)
	}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}