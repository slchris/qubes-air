package main

import (
	"context"
	"fmt"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
)

func preflightDatabase(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := database.CheckFile(ctx, path)
	if err != nil {
		return err
	}
	fmt.Printf("database_schema=%d supported_schema=%d\n", version, database.SchemaVersion)
	return nil
}
