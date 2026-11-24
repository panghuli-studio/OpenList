package db

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func useTestDB(t *testing.T) {
	t.Helper()
	d, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	prev := db
	db = d
	t.Cleanup(func() { db = prev })
	if err := d.AutoMigrate(new(model.S3ObjectMeta)); err != nil {
		t.Fatal(err)
	}
}

func TestS3ObjectMetaUpsertAndTreeDelete(t *testing.T) {
	useTestDB(t)
	for _, p := range []string{"/m/a_b/x", "/m/a_b/y/z", "/m/axb/keep", "/m/a_b", "/m/a_bc"} {
		if err := SaveS3ObjectMeta(&model.S3ObjectMeta{Path: p, Size: 1, ETag: `"1"`}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveS3ObjectMeta(&model.S3ObjectMeta{Path: "/m/a_b/x", Size: 2, ETag: `"2"`, Meta: `{"K":"v"}`}); err != nil {
		t.Fatal(err)
	}
	got, err := GetS3ObjectMeta("/m/a_b/x")
	if err != nil || got == nil || got.Size != 2 || got.ETag != `"2"` || got.Meta != `{"K":"v"}` {
		t.Fatalf("after upsert: %+v, %v", got, err)
	}

	// "_" must not act as a LIKE wildcard: /m/axb/keep and the sibling /m/a_bc stay.
	if err := DeleteS3ObjectMetaTree("/m/a_b/"); err != nil {
		t.Fatal(err)
	}
	all, err := GetS3ObjectMetas([]string{"/m/a_b/x", "/m/a_b/y/z", "/m/axb/keep", "/m/a_b", "/m/a_bc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["/m/axb/keep"] == nil || all["/m/a_bc"] == nil {
		t.Fatalf("remaining records = %v, want only /m/axb/keep and /m/a_bc", all)
	}
	if missing, err := GetS3ObjectMeta("/m/a_b/x"); err != nil || missing != nil {
		t.Fatalf("deleted record still found: %+v, %v", missing, err)
	}
}
