// Copyright (C) 2026 Homer Server Contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package ducklake

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTieredStorageManualS3Move is an opt-in, live-S3 validation of the exact
// TieredStorageManager copy-then-delete path. It is deliberately inert in CI:
// running it requires HOMER_TIERING_S3_TEST=1 and an explicitly named,
// disposable tiering-validation S3 prefix.
func TestTieredStorageManualS3Move(t *testing.T) {
	if os.Getenv("HOMER_TIERING_S3_TEST") != "1" {
		t.Skip("set HOMER_TIERING_S3_TEST=1 to run the live S3 tiering validation")
	}

	basePath := strings.TrimSuffix(strings.TrimSpace(os.Getenv("HOMER_STORAGE_DUCKLAKE_STORAGE_POLICY_VOLUMES_1_PATH")), "/")
	if !strings.HasPrefix(basePath, "s3://") || !strings.Contains(basePath, "/tiering-validation-") {
		t.Fatalf("refusing live S3 validation outside a tiering-validation prefix")
	}

	region := strings.TrimSpace(os.Getenv("HOMER_STORAGE_DUCKLAKE_STORAGE_POLICY_VOLUMES_1_S3_REGION"))
	accessKey := strings.TrimSpace(os.Getenv("HOMER_STORAGE_DUCKLAKE_STORAGE_POLICY_VOLUMES_1_S3_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv("HOMER_STORAGE_DUCKLAKE_STORAGE_POLICY_VOLUMES_1_S3_SECRET_ACCESS_KEY"))
	for name, value := range map[string]string{
		"HOMER_STORAGE_DUCKLAKE_STORAGE_POLICY_VOLUMES_1_S3_REGION":            region,
		"HOMER_STORAGE_DUCKLAKE_STORAGE_POLICY_VOLUMES_1_S3_ACCESS_KEY_ID":     accessKey,
		"HOMER_STORAGE_DUCKLAKE_STORAGE_POLICY_VOLUMES_1_S3_SECRET_ACCESS_KEY": secretKey,
	} {
		if value == "" {
			t.Fatalf("missing required live-S3 environment variable %s", name)
		}
	}

	root := t.TempDir()
	hotPath := filepath.Join(root, "hot")
	spillPath := filepath.Join(root, "spill")
	if err := os.MkdirAll(hotPath, 0o755); err != nil {
		t.Fatalf("create hot path: %v", err)
	}
	if err := os.MkdirAll(spillPath, 0o755); err != nil {
		t.Fatalf("create spill path: %v", err)
	}

	config := TieredStorageConfig{
		Enable:              true,
		CatalogType:         CatalogSQLite,
		CatalogPath:         filepath.Join(root, "catalog.sqlite"),
		TuningThreads:       1,
		TuningMemoryLimit:   "512MiB",
		TuningTempDirectory: spillPath,
		Volumes: []Volume{
			{
				Name:     "hot",
				Type:     VolumeTypeLocal,
				Path:     hotPath,
				Priority: 0,
				LakeName: "tier_validate_hot",
			},
			{
				Name:        "cold",
				Type:        VolumeTypeS3,
				Path:        fmt.Sprintf("%s/manual-move-%d/", basePath, time.Now().UTC().UnixNano()),
				Priority:    1,
				LakeName:    "tier_validate_cold",
				S3Region:    region,
				S3AccessKey: accessKey,
				S3SecretKey: secretKey,
				S3UseSSL:    true,
			},
		},
	}

	manager, err := NewTieredStorageManager(config)
	if err != nil {
		t.Fatalf("NewTieredStorageManager: %v", err)
	}
	defer manager.Stop()
	if err := manager.Start(); err != nil {
		t.Fatalf("start tiered storage manager: %v", err)
	}

	volumes := manager.GetVolumes()
	if len(volumes) != 2 {
		t.Fatalf("attached volumes = %d, want 2", len(volumes))
	}
	hot, cold := volumes[0], volumes[1]
	const table = "hep_proto_1_tiering_validation"
	for _, lake := range []string{hot.LakeName, cold.LakeName} {
		stmt := fmt.Sprintf("CREATE TABLE %s.main.%s (date DATE, timestamp TIMESTAMP, payload VARCHAR)", lake, table)
		if _, err := manager.GetDB().Exec(stmt); err != nil {
			t.Fatalf("create %s table: %v", lake, err)
		}
	}

	const date = "2000-01-02"
	insert := fmt.Sprintf("INSERT INTO %s.main.%s VALUES (?, ?, ?)", hot.LakeName, table)
	if _, err := manager.GetDB().Exec(insert, date, "2000-01-02 03:04:05", "live S3 tiering validation"); err != nil {
		t.Fatalf("insert hot row: %v", err)
	}
	if err := manager.MovePartition(table, date, hot, cold); err != nil {
		t.Fatalf("MovePartition: %v", err)
	}

	countRows := func(lake string) int {
		t.Helper()
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s.main.%s WHERE date = ?", lake, table)
		var count int
		if err := manager.GetDB().QueryRow(query, date).Scan(&count); err != nil {
			t.Fatalf("count %s rows: %v", lake, err)
		}
		return count
	}
	if got := countRows(hot.LakeName); got != 0 {
		t.Fatalf("hot rows after move = %d, want 0", got)
	}
	if got := countRows(cold.LakeName); got != 1 {
		t.Fatalf("cold rows after move = %d, want 1", got)
	}

	if err := manager.Stop(); err != nil {
		t.Fatalf("stop first manager: %v", err)
	}
	manager, err = NewTieredStorageManager(config)
	if err != nil {
		t.Fatalf("reopen manager: %v", err)
	}
	defer manager.Stop()
	if err := manager.Start(); err != nil {
		t.Fatalf("restart tiered storage manager: %v", err)
	}
	var reread int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s.main.%s WHERE date = ?", cold.LakeName, table)
	if err := manager.GetDB().QueryRow(query, date).Scan(&reread); err != nil {
		t.Fatalf("re-read cold row after reattach: %v", err)
	}
	if reread != 1 {
		t.Fatalf("cold rows after reattach = %d, want 1", reread)
	}
}
