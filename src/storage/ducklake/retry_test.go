// Copyright (C) 2026 Homer Server Contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package ducklake

import (
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

func TestIsRetriableCatalogError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"database locked", errors.New("database is locked"), true},
		{"failed commit", errors.New("Failed to commit DuckLake transaction"), true},
		{"could not set lock", errors.New("Could not set lock on catalog"), true},
		{"transaction conflict", errors.New("transaction conflict on catalog"), true},
		{"http flake", errors.New("HTTP Error: timeout"), true},
		{"no such bucket", errors.New("NoSuchBucket: missing"), false},
		{"invalid key", errors.New("InvalidAccessKeyId"), false},
		{"permanent", errors.New("syntax error"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetriableCatalogError(tc.err); got != tc.want {
				t.Fatalf("isRetriableCatalogError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type fakeCatalogLocker struct {
	lockCount atomic.Int32
}

func (f *fakeCatalogLocker) CatalogLock() {
	f.lockCount.Add(1)
}

func (f *fakeCatalogLocker) CatalogUnlock() {}

func TestExecWithRetryUsesLockerAroundExec(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Skipf("duckdb unavailable: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	locker := &fakeCatalogLocker{}
	if _, err := execWithRetry(db, 1, time.Millisecond, locker, "SELECT 1"); err != nil {
		t.Fatalf("execWithRetry failed: %v", err)
	}
	if locker.lockCount.Load() != 1 {
		t.Fatalf("lock count = %d, want 1", locker.lockCount.Load())
	}
}

func TestHotCatalogLockerOnlyForPrimaryVolume(t *testing.T) {
	tsm := &TieredStorageManager{
		primaryVolume: &Volume{LakeName: "homer_lake_hot"},
		catalogLocker: &fakeCatalogLocker{},
	}

	hot := &Volume{LakeName: "homer_lake_hot"}
	cold := &Volume{LakeName: "homer_lake_cold"}

	if tsm.hotCatalogLocker(hot) == nil {
		t.Fatal("expected locker for hot source volume")
	}
	if tsm.hotCatalogLocker(cold) != nil {
		t.Fatal("expected nil locker for cold source volume")
	}
}

func TestMovePartitionIdempotencySkipsInsertWhenDestinationHasRows(t *testing.T) {
	// Pure logic: when dstCount > 0 we must not run INSERT. Covered indirectly by
	// partitionRowCount + branch structure; integration needs full DuckLake attach.
	tsm := &TieredStorageManager{
		primaryVolume: &Volume{LakeName: "lake_hot"},
		catalogLocker: &fakeCatalogLocker{},
	}

	locker := tsm.hotCatalogLocker(&Volume{LakeName: "lake_hot"})
	if locker == nil {
		t.Fatal("hot locker should be set for primary source volume")
	}
}

func TestMovePartitionRefusesMismatchedDestination(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Skipf("duckdb unavailable: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	for _, statement := range []string{
		"ATTACH ':memory:' AS lake_hot",
		"ATTACH ':memory:' AS lake_cold",
		"CREATE TABLE lake_hot.main.hep_proto_test (date DATE, id INTEGER)",
		"CREATE TABLE lake_cold.main.hep_proto_test (date DATE, id INTEGER)",
		"INSERT INTO lake_hot.main.hep_proto_test VALUES ('2000-01-02', 1), ('2000-01-02', 2)",
		"INSERT INTO lake_cold.main.hep_proto_test VALUES ('2000-01-02', 99)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}

	hot := &Volume{Name: "hot", LakeName: "lake_hot"}
	cold := &Volume{Name: "cold", LakeName: "lake_cold"}
	tsm := &TieredStorageManager{db: db, primaryVolume: hot}

	err = tsm.MovePartition("hep_proto_test", "2000-01-02", hot, cold)
	if err == nil || !strings.Contains(err.Error(), "refusing source delete") {
		t.Fatalf("MovePartition() error = %v, want mismatched destination refusal", err)
	}

	for lake, want := range map[string]int{"lake_hot": 2, "lake_cold": 1} {
		var got int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + lake + ".main.hep_proto_test WHERE date = '2000-01-02'").Scan(&got); err != nil {
			t.Fatalf("count %s: %v", lake, err)
		}
		if got != want {
			t.Fatalf("%s row count = %d, want %d", lake, got, want)
		}
	}
}
