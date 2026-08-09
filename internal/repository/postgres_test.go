package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgresPersistenceEndToEnd exercises the real repository flows against a
// real Postgres and verifies everything survives a full "server restart"
// (fresh DB instance loading from the same database).
//
// It runs only when TEST_DATABASE_URL is set (e.g. a local Postgres instance),
// and skips otherwise so plain `go test ./...` stays fast and dependency-free.
func TestPostgresPersistenceEndToEnd(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}

	// Wipe everything for a clean, deterministic slate.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS kv`); err != nil {
		t.Fatalf("drop kv: %v", err)
	}
	pool.Close()

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ORBIT_JWT_SECRET", "")
	t.Setenv("ORBIT_INVITE_SALT", "")

	// ---- First "boot": create the full dataset ----
	db1, err := New(t.TempDir() + "/orbit.db")
	if err != nil {
		t.Fatalf("New (boot 1): %v", err)
	}

	alice, err := db1.UpsertUser("user-alice", "Alice", "alice@example.com", "pro", "LIC-ALICE")
	if err != nil {
		t.Fatalf("UpsertUser alice: %v", err)
	}
	if _, err := db1.UpsertUser("user-bob", "Bob", "bob@example.com", "pro", "LIC-BOB"); err != nil {
		t.Fatalf("UpsertUser bob: %v", err)
	}

	// Friends: request + accept keeps data on BOTH sides.
	fr, err := db1.SendFriendRequest(alice.ID, "user-bob")
	if err != nil {
		t.Fatalf("SendFriendRequest: %v", err)
	}
	if err := db1.AcceptFriendRequest(fr.ID, "user-bob"); err != nil {
		t.Fatalf("AcceptFriendRequest: %v", err)
	}

	// Project + membership (the "peers vanish" bug).
	proj, err := db1.CreateProject("orbit-app", "TypeScript", "web", alice.ID)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := db1.InviteMember(proj.ID, "user-bob"); err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	if err := db1.UpdateMemberPath(proj.ID, "user-bob", "apps/desktop-ui"); err != nil {
		t.Fatalf("UpdateMemberPath: %v", err)
	}

	// Encrypted Cloud Relay: push a delta, have a peer pull it, then ack it so
	// the blob clears once all members are caught up.
	delta, err := db1.StoreDelta(proj.ID, "user-alice", "BASE64_E2EE_BLOB")
	if err != nil {
		t.Fatalf("StoreDelta: %v", err)
	}
	pulled, err := db1.GetDeltas(proj.ID, time.Time{})
	if err != nil || len(pulled) != 1 {
		t.Fatalf("GetDeltas: n=%d err=%v", len(pulled), err)
	}
	if err := db1.AckDelta(proj.ID, delta.ID, "user-bob"); err != nil {
		t.Fatalf("AckDelta: %v", err)
	}
	afterAck, err := db1.GetDeltas(proj.ID, time.Time{})
	if err != nil {
		t.Fatalf("GetDeltas after ack: %v", err)
	}
	if len(afterAck) != 0 {
		t.Fatalf("relay blob not cleared after all members acked: %d remain", len(afterAck))
	}

	// Tasks.
	task, err := db1.CreateTask(proj.ID, "Fix sync", "user-bob", alice.ID)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := db1.CompleteTask(proj.ID, task.ID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	// Chat.
	if _, err := db1.SaveMessage(proj.ID, "user-alice", "hello"); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	// Signaling.
	if err := db1.SaveSignal(proj.ID, "user-alice", "user-bob", "offer", "sdp"); err != nil {
		t.Fatalf("SaveSignal: %v", err)
	}

	// Activity/pulse.
	if err := db1.LogActivity("user-bob", proj.ID, "task_completed"); err != nil {
		t.Fatalf("LogActivity: %v", err)
	}

	// Secrets must be stable across boots.
	jwt1 := db1.GetOrCreateSecret("jwt", 32)
	salt1 := db1.GetOrCreateSecret("invite", 16)
	if jwt1 == "" || salt1 == "" {
		t.Fatalf("secrets not generated")
	}

	// Simulate a clean shutdown (final save under lock).
	if err := db1.Shutdown(); err != nil {
		t.Fatalf("Shutdown (boot 1): %v", err)
	}

	// ---- Second "boot": everything must still be there ----
	db2, err := New(t.TempDir() + "/orbit.db")
	if err != nil {
		t.Fatalf("New (boot 2): %v", err)
	}
	defer db2.Close()

	checkUser(t, db2, alice.ID, "Alice")
	checkUser(t, db2, "user-bob", "Bob")

	friends, err := db2.GetFriends(alice.ID)
	if err != nil {
		t.Fatalf("GetFriends (boot 2): %v", err)
	}
	if len(friends) != 1 || friends[0].ID != "user-bob" {
		t.Fatalf("friends lost across restart: got %+v", friends)
	}

	projects, err := db2.ListProjectsForUser(alice.ID)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects lost across restart: n=%d err=%v", len(projects), err)
	}
	if !db2.IsProjectMember(proj.ID, "user-bob") {
		t.Fatalf("project member bob lost across restart")
	}

	members, err := db2.GetProjectMembers(proj.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("project members lost across restart: n=%d err=%v", len(members), err)
	}
	if members[0].Path == "" && members[1].Path == "" {
		t.Fatalf("member paths lost across restart")
	}

	tasks, err := db2.GetTasks(proj.ID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != "completed" {
		t.Fatalf("task state lost across restart: %+v err=%v", tasks, err)
	}

	msgs, err := db2.GetMessages(proj.ID, 0, 100)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Fatalf("messages lost across restart: %+v err=%v", msgs, err)
	}

	sigs, err := db2.GetPendingSignalsForPeer(proj.ID, "user-bob")
	if err != nil || len(sigs) != 1 {
		t.Fatalf("signals lost across restart: %+v err=%v", sigs, err)
	}

	pulse, err := db2.GetPulse("user-bob")
	if err != nil || len(pulse) != 1 {
		t.Fatalf("pulse lost across restart: %+v err=%v", pulse, err)
	}

	relayed, err := db2.GetDeltas(proj.ID, time.Time{})
	if err != nil || len(relayed) != 0 {
		t.Fatalf("relay state changed across restart: n=%d err=%v", len(relayed), err)
	}

	// A new push after restart must still flow through the relay correctly.
	delta2, err := db2.StoreDelta(proj.ID, "user-alice", "BLOB_2")
	if err != nil {
		t.Fatalf("StoreDelta (boot 2): %v", err)
	}
	if err := db2.AckDelta(proj.ID, delta2.ID, "user-bob"); err != nil {
		t.Fatalf("AckDelta (boot 2): %v", err)
	}

	if got := db2.GetOrCreateSecret("jwt", 32); got != jwt1 {
		t.Fatalf("jwt secret changed across restart: %q != %q", got, jwt1)
	}
	if got := db2.GetOrCreateSecret("invite", 16); got != salt1 {
		t.Fatalf("invite salt changed across restart: %q != %q", got, salt1)
	}

	// Presence sweep must also work after restart.
	if err := db2.UpdatePresence("user-bob", "coding"); err != nil {
		t.Fatalf("UpdatePresence (boot 2): %v", err)
	}
	if err := db2.UpdateStatus("user-bob", "offline"); err != nil {
		t.Fatalf("UpdateStatus (boot 2): %v", err)
	}
}

func checkUser(t *testing.T, db *DB, id, wantName string) {
	t.Helper()
	u, err := db.GetUserByID(id)
	if err != nil || u == nil {
		t.Fatalf("user %s lost across restart: err=%v", id, err)
	}
	if u.DisplayName != wantName {
		t.Fatalf("user %s name %q != %q", id, u.DisplayName, wantName)
	}
}

// TestPostgresSecretsEnvOverride verifies the env override still wins.
func TestPostgresSecretsEnvOverride(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ORBIT_JWT_SECRET", "env-var-jwt")

	db, err := New(t.TempDir() + "/orbit.db")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	if got := db.GetOrCreateSecret("jwt", 32); got != "env-var-jwt" {
		t.Fatalf("env override ignored: got %q", got)
	}
}
