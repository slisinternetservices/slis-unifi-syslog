package backup

import (
	"errors"
	"io"
	"os"

	"filippo.io/age"
)

// encryptFile encrypts src to dst with a passphrase (age: scrypt key
// derivation, ChaCha20-Poly1305). The result opens with the age tool too:
// age -d -o backup.tar backup.tar.age
func encryptFile(src, dst, passphrase string) error {
	if passphrase == "" {
		return errors.New("no encryption passphrase set")
	}
	rcp, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst + ".part")
	if err != nil {
		return err
	}
	w, err := age.Encrypt(out, rcp)
	if err == nil {
		_, err = io.Copy(w, in)
	}
	if err == nil {
		err = w.Close()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst + ".part")
		return err
	}
	return os.Rename(dst+".part", dst)
}

func decryptFile(src, dst, passphrase string) error {
	if passphrase == "" {
		return errors.New("this backup is encrypted; enter its passphrase")
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r, err := age.Decrypt(in, id)
	if err != nil {
		var nm *age.NoIdentityMatchError
		if errors.As(err, &nm) {
			return errors.New("wrong passphrase")
		}
		return err
	}
	return writeFile(dst, r)
}
