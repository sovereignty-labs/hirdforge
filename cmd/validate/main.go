// validate is a standalone tool that validates agent deployment YAML files.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dir := flag.String("dir", "", "Directory containing deployment YAML files")
	flag.Parse()

	if *dir == "" {
		fmt.Println("Usage: validate -dir <path>")
		os.Exit(1)
	}

	var yamlFiles []string
	err := filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".yaml") {
			yamlFiles = append(yamlFiles, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error walking directory: %v\n", err)
		os.Exit(1)
	}

	if len(yamlFiles) == 0 {
		fmt.Println("No .yaml files found")
		os.Exit(0)
	}

	var totalPass, totalFail int
	for _, f := range yamlFiles {
		results := validateFile(f)
		pass := results["kind"] && results["image"] && results["serviceAccount"] && results["port"] && results["tag"]
		if pass {
			totalPass++
		} else {
			totalFail++
		}
		printResult(f, results, pass)
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Total files: %d\n", len(yamlFiles))
	fmt.Printf("Passing: %d\n", totalPass)
	fmt.Printf("With issues: %d\n", totalFail)

	if totalFail > 0 {
		os.Exit(1)
	}
}

func validateFile(path string) map[string]bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	lines := strings.Split(string(content), "\n")

	results := map[string]bool{
		"kind":           false,
		"image":          false,
		"serviceAccount": false,
		"port":           false,
		"tag":            true,
	}

	for _, line := range lines {
		if strings.Contains(line, "kind:") && strings.Contains(line, "Deployment") {
			results["kind"] = true
		}
		if strings.Contains(line, "image:") {
			val := strings.TrimSpace(strings.SplitN(line, "image:", 2)[1])
			val = strings.TrimSpace(val)
			if val != "" {
				results["image"] = true
				if strings.HasSuffix(val, ":latest") {
					results["tag"] = false
				}
			}
		}
		if strings.Contains(line, "serviceAccountName:") {
			results["serviceAccount"] = true
		}
		if strings.Contains(line, "containerPort:") {
			results["port"] = true
		}
	}
	return results
}

func printResult(path string, results map[string]bool, pass bool) {
	status := "PASS"
	if !pass {
		status = "FAIL"
	}
	fmt.Printf("\n[%s] %s\n", status, filepath.Base(path))
	fmt.Printf("  kind: Deployment        %v\n", results["kind"])
	fmt.Printf("  image: present         %v\n", results["image"])
	fmt.Printf("  serviceAccountName:    %v\n", results["serviceAccount"])
	fmt.Printf("  containerPort:        %v\n", results["port"])
	fmt.Printf("  tag not :latest       %v\n", results["tag"])
}
