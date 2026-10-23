package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func TestBackupNames(t *testing.T) {
	for n, want := range map[string]bool{
		"unify-syslog-20261019-120000-full-abcd1234.tar":                   true,
		"unify-syslog-20261019-120000-full-abcd1234.tar.age":               true,
		"unify-syslog-20261019-120000-incr-abcd1234-base-0123abcd.tar.age": true,
		"unify-syslog-20261019-120000-full-abcd1234.tar.part":              false,
		"other.tar": false,
		"unify-syslog-20261019-120000-full-abcd1234.tar.age/../../etc/passwd": false,
	} {
		if got := IsBackupName(n); got != want {
			t.Errorf("IsBackupName(%q) = %v", n, got)
		}
	}
	e, ok := parseName(RemoteFile{Name: "unify-syslog-20261019-120000-incr-abcd1234-base-0123abcd.tar.age"})
	if !ok || e.Kind != "incremental" || e.BaseID != "0123abcd" || e.ID != "abcd1234" {
		t.Errorf("parseName: %+v %v", e, ok)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "a.tar")
	data := bytes.Repeat([]byte("unifi syslog backup "), 50000)
	os.WriteFile(plain, data, 0o600)
	if err := encryptFile(plain, plain+".age", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	enc, _ := os.ReadFile(plain + ".age")
	if bytes.Contains(enc, []byte("unifi syslog backup")) {
		t.Fatal("archive is not encrypted")
	}
	if err := decryptFile(plain+".age", filepath.Join(dir, "wrong.tar"), "wrong passphrase!"); err == nil || err.Error() != "wrong passphrase" {
		t.Fatalf("wrong passphrase: %v", err)
	}
	out := filepath.Join(dir, "b.tar")
	if err := decryptFile(plain+".age", out, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, data) {
		t.Fatal("decrypted data differs")
	}
}

// testRemote uploads, lists, downloads and deletes through a Remote.
func testRemote(t *testing.T, target store.BackupTarget) {
	ctx := context.Background()
	msg, err := Test(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(msg)
	r, err := Connect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	src := filepath.Join(t.TempDir(), "x")
	data := bytes.Repeat([]byte{1, 2, 3}, 100000)
	os.WriteFile(src, data, 0o600)
	name := "unify-syslog-20261019-120000-full-abcd1234.tar.age"
	if err := r.Upload(ctx, src, name); err != nil {
		t.Fatal(err)
	}
	// Uploading again replaces the file.
	if err := r.Upload(ctx, src, name); err != nil {
		t.Fatal(err)
	}
	files, err := r.List(ctx)
	if err != nil || len(files) != 1 || files[0].Name != name || files[0].Size != int64(len(data)) {
		t.Fatalf("list: %+v %v", files, err)
	}
	dst := filepath.Join(t.TempDir(), "y")
	if err := r.Download(ctx, name, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("downloaded data differs")
	}
	if err := r.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	if files, _ := r.List(ctx); len(files) != 0 {
		t.Fatalf("after delete: %+v", files)
	}
}

func TestSFTPRemote(t *testing.T) {
	root := t.TempDir()
	addr, fp := startSFTP(t, "backup", "s3cret", root)
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	target := store.BackupTarget{Type: "sftp", Host: host, Port: p, Username: "backup", Password: "s3cret", HostKey: fp, Path: filepath.Join(root, "nested/dir")}
	testRemote(t, target)
	target.HostKey = "SHA256:wrong"
	if _, err := Connect(context.Background(), target); err == nil {
		t.Fatal("host key mismatch accepted")
	}
}

func TestS3Remote(t *testing.T) {
	ep := os.Getenv("USS_TEST_S3")
	if ep == "" {
		t.Skip("USS_TEST_S3 not set (S3 endpoint with a bucket named backups)")
	}
	testRemote(t, store.BackupTarget{Type: "s3", Endpoint: ep, Bucket: "backups", Path: "unify/test", Username: "a", Password: "b", Region: "us-east-1"})
}

// startSFTP runs an SSH server with only the sftp subsystem, serving the
// real filesystem, and returns its address and host key fingerprint.
func startSFTP(t *testing.T, user, pass, root string) (string, string) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == user && string(p) == pass {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					c, creqs, err := ch.Accept()
					if err != nil {
						continue
					}
					go func() {
						for req := range creqs {
							ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
							req.Reply(ok, nil)
							if ok {
								srv, _ := sftp.NewServer(c)
								srv.Serve()
								c.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}
