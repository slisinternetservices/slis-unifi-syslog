package backup

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	scp "github.com/bramvdbogaerde/go-scp"
	"github.com/jlaffaye/ftp"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

type RemoteFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type Remote interface {
	Upload(ctx context.Context, local, name string) error
	Download(ctx context.Context, name, local string) error
	List(ctx context.Context) ([]RemoteFile, error)
	Delete(ctx context.Context, name string) error
	Close() error
}

func Connect(ctx context.Context, t store.BackupTarget) (Remote, error) {
	switch t.Type {
	case "ftp", "ftps", "ftps-implicit":
		return dialFTP(ctx, t)
	case "scp":
		return dialSCP(ctx, t)
	case "sftp":
		return dialSFTP(ctx, t)
	case "s3":
		return dialS3(ctx, t)
	case "local":
		if t.Path == "" {
			return nil, errors.New("path is required")
		}
		if err := os.MkdirAll(t.Path, 0o750); err != nil {
			return nil, err
		}
		return &localRemote{dir: t.Path}, nil
	}
	return nil, fmt.Errorf("unknown target type %q", t.Type)
}

// IsBackupName tells whether a file name is a backup archive, plain
// (.tar) or encrypted (.tar.age).
func IsBackupName(n string) bool {
	return strings.HasPrefix(n, "unify-syslog-") && (strings.HasSuffix(n, ".tar") || strings.HasSuffix(n, ".tar.age")) && !strings.ContainsAny(n, "/\\")
}

// ---------------------------------------------------------------- FTP / FTPS

type ftpRemote struct {
	c   *ftp.ServerConn
	dir string
}

func dialFTP(ctx context.Context, t store.BackupTarget) (Remote, error) {
	port := t.Port
	if port == 0 {
		port = 21
		if t.Type == "ftps-implicit" {
			port = 990
		}
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(port))
	tlsCfg := &tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.SkipVerify,
		// Many FTPS servers require the data connection to resume the control session.
		ClientSessionCache: tls.NewLRUClientSessionCache(8),
		// With TLS 1.3 servers send session tickets on the data connection
		// after the handshake; closing with those unread makes the kernel send
		// a RST, and the server then discards the tail of the upload.
		MaxVersion: tls.VersionTLS12}
	opts := []ftp.DialOption{ftp.DialWithContext(ctx), ftp.DialWithTimeout(30 * time.Second)}
	switch t.Type {
	case "ftps":
		opts = append(opts, ftp.DialWithExplicitTLS(tlsCfg))
	case "ftps-implicit":
		opts = append(opts, ftp.DialWithTLS(tlsCfg))
	}
	c, err := ftp.Dial(addr, opts...)
	if err != nil {
		return nil, err
	}
	user := t.Username
	if user == "" {
		user = "anonymous"
	}
	if err := c.Login(user, t.Password); err != nil {
		c.Quit()
		return nil, fmt.Errorf("login: %w", err)
	}
	dir := t.Path
	if dir == "" {
		dir = "."
	}
	if dir != "." {
		mkdirAllFTP(c, dir)
	}
	return &ftpRemote{c: c, dir: dir}, nil
}

func mkdirAllFTP(c *ftp.ServerConn, dir string) {
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	cur := ""
	if strings.HasPrefix(dir, "/") {
		cur = "/"
	}
	for _, p := range parts {
		cur = path.Join(cur, p)
		c.MakeDir(cur)
	}
}

func (r *ftpRemote) p(name string) string { return path.Join(r.dir, name) }

func (r *ftpRemote) Upload(ctx context.Context, local, name string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	tmp := r.p(name + ".part")
	if err := r.c.Stor(tmp, f); err != nil {
		return err
	}
	if n, err := r.c.FileSize(tmp); err == nil && n != st.Size() {
		r.c.Delete(tmp)
		return fmt.Errorf("upload incomplete: %d of %d bytes", n, st.Size())
	}
	r.c.Delete(r.p(name))
	return r.c.Rename(tmp, r.p(name))
}

func (r *ftpRemote) Download(ctx context.Context, name, local string) error {
	resp, err := r.c.Retr(r.p(name))
	if err != nil {
		return err
	}
	defer resp.Close()
	return writeFile(local, resp)
}

func (r *ftpRemote) List(ctx context.Context) ([]RemoteFile, error) {
	entries, err := r.c.List(r.dir)
	if err != nil {
		return nil, err
	}
	var out []RemoteFile
	for _, e := range entries {
		if e.Type == ftp.EntryTypeFile && IsBackupName(e.Name) {
			out = append(out, RemoteFile{Name: e.Name, Size: int64(e.Size), Modified: e.Time})
		}
	}
	sortFiles(out)
	return out, nil
}

func (r *ftpRemote) Delete(ctx context.Context, name string) error { return r.c.Delete(r.p(name)) }
func (r *ftpRemote) Close() error                                  { return r.c.Quit() }

// ---------------------------------------------------------------- SCP (SSH)

type scpRemote struct {
	ssh *ssh.Client
	dir string
}

// dialSSH opens the SSH connection of an scp or sftp target.
func dialSSH(ctx context.Context, t store.BackupTarget) (*ssh.Client, error) {
	var auths []ssh.AuthMethod
	if strings.TrimSpace(t.PrivateKey) != "" {
		var signer ssh.Signer
		var err error
		if t.Password != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(t.PrivateKey), []byte(t.Password))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(t.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	} else if t.Password != "" {
		auths = append(auths, ssh.Password(t.Password), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			a := make([]string, len(qs))
			for i := range a {
				a[i] = t.Password
			}
			return a, nil
		}))
	}
	hostKeyCB := ssh.InsecureIgnoreHostKey()
	if fp := strings.TrimSpace(t.HostKey); fp != "" {
		hostKeyCB = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != fp {
				return fmt.Errorf("host key mismatch: got %s", ssh.FingerprintSHA256(key))
			}
			return nil
		}
	}
	port := t.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{User: t.Username, Auth: auths, HostKeyCallback: hostKeyCB, Timeout: 30 * time.Second}
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(t.Host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, conn.RemoteAddr().String(), cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

func dialSCP(ctx context.Context, t store.BackupTarget) (Remote, error) {
	cl, err := dialSSH(ctx, t)
	if err != nil {
		return nil, err
	}
	dir := t.Path
	if dir == "" {
		dir = "."
	}
	r := &scpRemote{ssh: cl, dir: dir}
	if _, err := r.run("mkdir -p " + shellQuote(dir)); err != nil {
		cl.Close()
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	return r, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (r *scpRemote) run(cmd string) (string, error) {
	s, err := r.ssh.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	var out, errb bytes.Buffer
	s.Stdout, s.Stderr = &out, &errb
	if err := s.Run(cmd); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (r *scpRemote) client() (scp.Client, error) {
	c, err := scp.NewClientBySSH(r.ssh)
	return c, err
}

func (r *scpRemote) Upload(ctx context.Context, local, name string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	c, err := r.client()
	if err != nil {
		return err
	}
	defer c.Close()
	tmp := path.Join(r.dir, name+".part")
	if err := c.CopyFromFile(ctx, *f, tmp, "0640"); err != nil {
		return err
	}
	_, err = r.run("mv -f " + shellQuote(tmp) + " " + shellQuote(path.Join(r.dir, name)))
	return err
}

func (r *scpRemote) Download(ctx context.Context, name, local string) error {
	c, err := r.client()
	if err != nil {
		return err
	}
	defer c.Close()
	f, err := os.Create(local)
	if err != nil {
		return err
	}
	defer f.Close()
	return c.CopyFromRemote(ctx, f, path.Join(r.dir, name))
}

func (r *scpRemote) List(ctx context.Context) ([]RemoteFile, error) {
	// Portable: name, size and mtime via find (GNU/BusyBox) with ls fallback.
	out, err := r.run("cd " + shellQuote(r.dir) + " && for f in unify-syslog-*.tar unify-syslog-*.tar.age; do [ -f \"$f\" ] && printf '%s\\t%s\\t%s\\n' \"$f\" \"$(wc -c <\"$f\")\" \"$(date -r \"$f\" +%s 2>/dev/null || echo 0)\"; done; true")
	if err != nil {
		return nil, err
	}
	var files []RemoteFile
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		p := strings.Split(line, "\t")
		if len(p) != 3 || !IsBackupName(p[0]) {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(p[1]), 10, 64)
		mt, _ := strconv.ParseInt(strings.TrimSpace(p[2]), 10, 64)
		files = append(files, RemoteFile{Name: p[0], Size: size, Modified: time.Unix(mt, 0)})
	}
	sortFiles(files)
	return files, nil
}

func (r *scpRemote) Delete(ctx context.Context, name string) error {
	_, err := r.run("rm -f " + shellQuote(path.Join(r.dir, name)))
	return err
}

func (r *scpRemote) Close() error { return r.ssh.Close() }

// ---------------------------------------------------------------- SFTP

type sftpRemote struct {
	ssh *ssh.Client
	c   *sftp.Client
	dir string
}

func dialSFTP(ctx context.Context, t store.BackupTarget) (Remote, error) {
	cl, err := dialSSH(ctx, t)
	if err != nil {
		return nil, err
	}
	c, err := sftp.NewClient(cl)
	if err != nil {
		cl.Close()
		return nil, fmt.Errorf("sftp: %w", err)
	}
	dir := t.Path
	if dir == "" {
		dir = "."
	}
	if err := c.MkdirAll(dir); err != nil {
		c.Close()
		cl.Close()
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	return &sftpRemote{ssh: cl, c: c, dir: dir}, nil
}

func (r *sftpRemote) p(name string) string { return path.Join(r.dir, name) }

func (r *sftpRemote) Upload(ctx context.Context, local, name string) error {
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := r.p(name + ".part")
	dst, err := r.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := dst.ReadFrom(src); err != nil {
		dst.Close()
		r.c.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	// PosixRename replaces an existing file where the server supports it.
	if err := r.c.PosixRename(tmp, r.p(name)); err != nil {
		r.c.Remove(r.p(name))
		return r.c.Rename(tmp, r.p(name))
	}
	return nil
}

func (r *sftpRemote) Download(ctx context.Context, name, local string) error {
	f, err := r.c.Open(r.p(name))
	if err != nil {
		return err
	}
	defer f.Close()
	return writeFile(local, f)
}

func (r *sftpRemote) List(ctx context.Context) ([]RemoteFile, error) {
	entries, err := r.c.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	var out []RemoteFile
	for _, e := range entries {
		if e.Mode().IsRegular() && IsBackupName(e.Name()) {
			out = append(out, RemoteFile{Name: e.Name(), Size: e.Size(), Modified: e.ModTime()})
		}
	}
	sortFiles(out)
	return out, nil
}

func (r *sftpRemote) Delete(ctx context.Context, name string) error { return r.c.Remove(r.p(name)) }

func (r *sftpRemote) Close() error {
	r.c.Close()
	return r.ssh.Close()
}

// ---------------------------------------------------------------- S3

type s3Remote struct {
	c      *minio.Client
	bucket string
	prefix string
}

func dialS3(ctx context.Context, t store.BackupTarget) (Remote, error) {
	u, err := url.Parse(t.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("the endpoint must be a URL such as https://s3.eu-central-1.wasabisys.com")
	}
	if t.Bucket == "" {
		return nil, errors.New("a bucket name is required")
	}
	tr, err := minio.DefaultTransport(u.Scheme == "https")
	if err != nil {
		return nil, err
	}
	if t.SkipVerify && tr.TLSClientConfig != nil {
		tr.TLSClientConfig.InsecureSkipVerify = true
	}
	c, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(t.Username, t.Password, ""), Secure: u.Scheme == "https",
		Region: t.Region, Transport: tr, BucketLookup: minio.BucketLookupAuto})
	if err != nil {
		return nil, err
	}
	ok, err := c.BucketExists(ctx, t.Bucket)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("bucket %s does not exist", t.Bucket)
	}
	prefix := strings.Trim(t.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &s3Remote{c: c, bucket: t.Bucket, prefix: prefix}, nil
}

func (r *s3Remote) Upload(ctx context.Context, local, name string) error {
	// Multipart upload with 64 MiB parts: archives can be many gigabytes.
	_, err := r.c.FPutObject(ctx, r.bucket, r.prefix+name, local, minio.PutObjectOptions{ContentType: "application/octet-stream", PartSize: 64 << 20})
	return err
}

func (r *s3Remote) Download(ctx context.Context, name, local string) error {
	obj, err := r.c.GetObject(ctx, r.bucket, r.prefix+name, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer obj.Close()
	return writeFile(local, obj)
}

func (r *s3Remote) List(ctx context.Context) ([]RemoteFile, error) {
	var out []RemoteFile
	for o := range r.c.ListObjects(ctx, r.bucket, minio.ListObjectsOptions{Prefix: r.prefix}) {
		if o.Err != nil {
			return nil, o.Err
		}
		name := strings.TrimPrefix(o.Key, r.prefix)
		if IsBackupName(name) {
			out = append(out, RemoteFile{Name: name, Size: o.Size, Modified: o.LastModified})
		}
	}
	sortFiles(out)
	return out, nil
}

func (r *s3Remote) Delete(ctx context.Context, name string) error {
	return r.c.RemoveObject(ctx, r.bucket, r.prefix+name, minio.RemoveObjectOptions{})
}

func (r *s3Remote) Close() error { return nil }

// ---------------------------------------------------------------- local

type localRemote struct{ dir string }

func (r *localRemote) Upload(ctx context.Context, local, name string) error {
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()
	return writeFile(filepath.Join(r.dir, name), src)
}

func (r *localRemote) Download(ctx context.Context, name, local string) error {
	src, err := os.Open(filepath.Join(r.dir, filepath.Base(name)))
	if err != nil {
		return err
	}
	defer src.Close()
	return writeFile(local, src)
}

func (r *localRemote) List(ctx context.Context) ([]RemoteFile, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	var out []RemoteFile
	for _, e := range entries {
		if e.IsDir() || !IsBackupName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, RemoteFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	sortFiles(out)
	return out, nil
}

func (r *localRemote) Delete(ctx context.Context, name string) error {
	return os.Remove(filepath.Join(r.dir, filepath.Base(name)))
}
func (r *localRemote) Close() error { return nil }

func writeFile(dst string, src io.Reader) error {
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func sortFiles(f []RemoteFile) {
	sort.Slice(f, func(i, j int) bool { return f[i].Name > f[j].Name })
}
