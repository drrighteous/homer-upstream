// Copyright (C) 2026 Homer Server Contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package writer

import (
	"strings"
	"testing"

	"github.com/sipcapture/homer-core/src/storage/ducklake"
)

func TestValidateTestOnlyMoveConfig(t *testing.T) {
	volumes := []*ducklake.Volume{
		{Name: "hot", Type: ducklake.VolumeTypeLocal, MaxDataAgeDays: 0, MaxSizeGB: 0},
		{Name: "cold", Type: ducklake.VolumeTypeS3, MaxDataAgeDays: 0, MaxSizeGB: 0},
	}

	valid := TieringConfig{
		MoveOnStartup:           true,
		TestOnlyTable:           "hep_proto_5_default",
		TestOnlyPartitionDate:   "2026-08-19",
		TestOnlyExpectedRows:    1000,
		TestOnlyMaxDataAgeDays:  1,
		TestOnlySkipMaintenance: true,
	}

	tests := []struct {
		name    string
		config  TieringConfig
		volumes []*ducklake.Volume
		wantErr string
	}{
		{name: "disabled by default", config: TieringConfig{}},
		{name: "valid closed day test gate", config: valid, volumes: volumes},
		{
			name:    "partial config fails closed",
			config:  TieringConfig{MoveOnStartup: true, TestOnlyTable: "hep_proto_5_default"},
			volumes: volumes,
			wantErr: "requires table",
		},
		{
			name: "invalid table rejected",
			config: func() TieringConfig {
				c := valid
				c.TestOnlyTable = "hep_proto_bad;drop"
				return c
			}(),
			volumes: volumes,
			wantErr: "must match",
		},
		{
			name:   "normal ttl trigger prohibited",
			config: valid,
			volumes: []*ducklake.Volume{
				{Name: "hot", Type: ducklake.VolumeTypeLocal, MaxDataAgeDays: 1},
				{Name: "cold", Type: ducklake.VolumeTypeS3},
			},
			wantErr: "normal max_data_age_days",
		},
		{
			name:   "size trigger prohibited",
			config: valid,
			volumes: []*ducklake.Volume{
				{Name: "hot", Type: ducklake.VolumeTypeLocal, MaxSizeGB: 1},
				{Name: "cold", Type: ducklake.VolumeTypeS3},
			},
			wantErr: "normal max_data_age_days",
		},
		{
			name: "startup trigger required",
			config: func() TieringConfig {
				c := valid
				c.MoveOnStartup = false
				return c
			}(),
			volumes: volumes,
			wantErr: "move_on_startup=true",
		},
		{
			name:   "volume types required",
			config: valid,
			volumes: []*ducklake.Volume{
				{Name: "hot", Type: ducklake.VolumeTypeS3},
				{Name: "cold", Type: ducklake.VolumeTypeS3},
			},
			wantErr: "hot local then cold s3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := &TieringService{config: tt.config}
			err := ts.validateTestOnlyMoveConfig(tt.volumes)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateTestOnlyMoveConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateTestOnlyMoveConfig() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
