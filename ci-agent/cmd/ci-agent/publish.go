package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/concourse/ci-agent/publish"
)

func runPublish(args []string) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	reviewPath := fs.String("review", "review/review.json", "path to review.json")
	costsPath := fs.String("costs", "", "path to costs.json (optional; missing file is skipped)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL:     os.Getenv("ATC_EXTERNAL_URL"),
		BuildID:    os.Getenv("BUILD_ID"),
		Token:      os.Getenv("AGENT_REVIEW_PUBLISH_TOKEN"),
		ReviewPath: *reviewPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "publish error: %v\n", err)
		return 1
	}
	fmt.Println("review published")

	if *costsPath != "" {
		err := publish.PublishCosts(context.Background(), publish.CostsOptions{
			ATCURL:    os.Getenv("ATC_EXTERNAL_URL"),
			BuildID:   os.Getenv("BUILD_ID"),
			Token:     os.Getenv("AGENT_REVIEW_PUBLISH_TOKEN"),
			CostsPath: *costsPath,
			Phase:     "review",
			UserName:  os.Getenv("AGENT_COST_USER"),
		})
		if err != nil {
			// Fire-and-forget: cost reporting never fails the build.
			fmt.Fprintf(os.Stderr, "warning: cost publish: %v\n", err)
		} else {
			fmt.Println("costs published")
		}
	}
	return 0
}
