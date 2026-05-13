package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openBootstrapDeviceTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := DB
	originalLOGDB := LOG_DB
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	initCol()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	LOG_DB = db

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		DB = originalDB
		LOG_DB = originalLOGDB
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		initCol()
	})

	return db
}

func TestBootstrapDeviceAutoMigrateCreatesExpectedSchema(t *testing.T) {
	db := openBootstrapDeviceTestDB(t)

	require.NoError(t, db.AutoMigrate(&BootstrapDevice{}))
	require.True(t, db.Migrator().HasTable(&BootstrapDevice{}))

	for _, column := range []string{
		"id",
		"install_id_hash",
		"device_fingerprint_hash",
		"user_id",
		"token_id",
		"status",
		"risk_flags",
		"first_ip",
		"last_ip",
		"user_agent",
		"client_version",
		"platform",
		"arch",
		"created_at",
		"updated_at",
		"last_seen_at",
	} {
		require.True(t, db.Migrator().HasColumn(&BootstrapDevice{}, column), "missing column %s", column)
	}
}

func TestBootstrapDeviceUniqueHashes(t *testing.T) {
	db := openBootstrapDeviceTestDB(t)
	require.NoError(t, db.AutoMigrate(&BootstrapDevice{}))

	first := &BootstrapDevice{
		InstallIDHash:         strings.Repeat("a", 64),
		DeviceFingerprintHash: strings.Repeat("b", 64),
		UserID:                1,
		TokenID:               1,
		Status:                BootstrapDeviceStatusActive,
	}
	require.NoError(t, db.Create(first).Error)

	duplicateInstall := &BootstrapDevice{
		InstallIDHash:         first.InstallIDHash,
		DeviceFingerprintHash: strings.Repeat("c", 64),
		UserID:                2,
		TokenID:               2,
		Status:                BootstrapDeviceStatusActive,
	}
	require.Error(t, db.Create(duplicateInstall).Error)

	duplicateFingerprint := &BootstrapDevice{
		InstallIDHash:         strings.Repeat("d", 64),
		DeviceFingerprintHash: first.DeviceFingerprintHash,
		UserID:                3,
		TokenID:               3,
		Status:                BootstrapDeviceStatusActive,
	}
	require.Error(t, db.Create(duplicateFingerprint).Error)
}
