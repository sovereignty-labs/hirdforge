package main

// Temporal Worker binary for Hirdforge.
// Registers the SovereignSession workflow and all agent activities.
// Connects to Temporal Server at the address specified via --temporal-addr flag.

import (
	"flag"
	"log"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/kitporath/project_valhalla/pkg/workflows"
)

func main() {
	temporalAddr := flag.String(
		"temporal-addr",
		getEnvOrDefault("TEMPORAL_ADDR", "temporal-server.asgard.svc:7233"),
		"Temporal server address",
	)
	namespace := flag.String(
		"namespace",
		getEnvOrDefault("TEMPORAL_NAMESPACE", "default"),
		"Temporal namespace",
	)
	flag.Parse()

	log.Printf("Temporal Worker starting")
	log.Printf("  Server: %s", *temporalAddr)
	log.Printf("  Namespace: %s", *namespace)
	log.Printf("  Task Queue: %s", workflows.TaskQueue)

	c, err := client.Dial(client.Options{
		HostPort:  *temporalAddr,
		Namespace: *namespace,
	})
	if err != nil {
		log.Fatalf("Failed to connect to Temporal: %v", err)
	}
	defer c.Close()

	w := worker.New(c, workflows.TaskQueue, worker.Options{})

	// Register workflow and all activities
	w.RegisterWorkflow(workflows.SovereignSessionWorkflow)
	activities := &workflows.AgentActivities{}
	w.RegisterActivity(activities.DispatchToAgent)
	w.RegisterActivity(activities.ClassifyMessage)

	log.Printf("Worker registered. Polling task queue: %s", workflows.TaskQueue)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("Worker exited with error: %v", err)
	}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
