package crypto

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/ProtonMail/gopenpgp/armor"
	"github.com/ProtonMail/gopenpgp/constants"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/packet"
)

// RandomToken generates a random token with the key size of the default cipher.
func (pgp *GopenPGP) RandomToken() ([]byte, error) {
	config := &packet.Config{DefaultCipher: packet.CipherAES256}
	keySize := config.DefaultCipher.KeySize()
	symKey := make([]byte, keySize)
	if _, err := io.ReadFull(config.Random(), symKey); err != nil {
		return nil, err
	}
	return symKey, nil
}

// RandomTokenWith generates a random token with the given key size.
func (pgp *GopenPGP) RandomTokenWith(size int) ([]byte, error) {
	config := &packet.Config{DefaultCipher: packet.CipherAES256}
	symKey := make([]byte, size)
	if _, err := io.ReadFull(config.Random(), symKey); err != nil {
		return nil, err
	}
	return symKey, nil
}

// GetSessionFromKeyPacket returns the decrypted session key from a binary
// public-key encrypted session key packet.
func (pgp *GopenPGP) GetSessionFromKeyPacket(
	keyPacket []byte, privateKey *KeyRing, passphrase string,
) (*SymmetricKey,
	error) {
	keyReader := bytes.NewReader(keyPacket)
	packets := packet.NewReader(keyReader)

	var p packet.Packet
	var err error
	if p, err = packets.Next(); err != nil {
		return nil, err
	}

	ek := p.(*packet.EncryptedKey)

	rawPwd := []byte(passphrase)
	var decryptErr error
	for _, key := range privateKey.entities.DecryptionKeys() {
		priv := key.PrivateKey
		if priv.Encrypted {
			if err := priv.Decrypt(rawPwd); err != nil {
				continue
			}
		}

		if decryptErr = ek.Decrypt(priv, nil); decryptErr == nil {
			break
		}
	}

	if decryptErr != nil {
		return nil, decryptErr
	}

	return getSessionSplit(ek)
}

// KeyPacketWithPublicKey encrypts the session key with the armored publicKey
// and returns a binary public-key encrypted session key packet.
func (pgp *GopenPGP) KeyPacketWithPublicKey(sessionSplit *SymmetricKey, publicKey string) ([]byte, error) {
	pubkeyRaw, err := armor.Unarmor(publicKey)
	if err != nil {
		return nil, err
	}
	return pgp.KeyPacketWithPublicKeyBin(sessionSplit, pubkeyRaw)
}

// KeyPacketWithPublicKeyBin encrypts the session key with the unarmored
// publicKey and returns a binary public-key encrypted session key packet.
func (pgp *GopenPGP) KeyPacketWithPublicKeyBin(sessionSplit *SymmetricKey, publicKey []byte) ([]byte, error) {
	publicKeyReader := bytes.NewReader(publicKey)
	pubKeyEntries, err := openpgp.ReadKeyRing(publicKeyReader)
	if err != nil {
		return nil, err
	}

	outbuf := &bytes.Buffer{}

	cf := sessionSplit.GetCipherFunc()

	if len(pubKeyEntries) == 0 {
		return nil, errors.New("cannot set key: key ring is empty")
	}

	var pub *packet.PublicKey
	for _, e := range pubKeyEntries {
		for _, subKey := range e.Subkeys {
			if !subKey.Sig.FlagsValid || subKey.Sig.FlagEncryptStorage || subKey.Sig.FlagEncryptCommunications {
				pub = subKey.PublicKey
				break
			}
		}
		if pub == nil && len(e.Identities) > 0 {
			var i *openpgp.Identity
			for _, i = range e.Identities {
				break
			}
			if i.SelfSignature.FlagsValid || i.SelfSignature.FlagEncryptStorage || i.SelfSignature.FlagEncryptCommunications {
				pub = e.PrimaryKey
			}
		}
		if pub != nil {
			break
		}
	}
	if pub == nil {
		return nil, errors.New("cannot set key: no public key available")
	}

	if err = packet.SerializeEncryptedKey(outbuf, pub, cf, sessionSplit.Key, nil); err != nil {
		err = fmt.Errorf("gopenpgp: cannot set key: %v", err)
		return nil, err
	}
	return outbuf.Bytes(), nil
}

// GetSessionFromSymmetricPacket decrypts the binary symmetrically encrypted
// session key packet and returns the session key.
func (pgp *GopenPGP) GetSessionFromSymmetricPacket(keyPacket []byte, password string) (*SymmetricKey, error) {
	keyReader := bytes.NewReader(keyPacket)
	packets := packet.NewReader(keyReader)

	var symKeys []*packet.SymmetricKeyEncrypted
	for {

		var p packet.Packet
		var err error
		if p, err = packets.Next(); err != nil {
			break
		}

		switch p := p.(type) {
		case *packet.SymmetricKeyEncrypted:
			symKeys = append(symKeys, p)
		}
	}

	pwdRaw := []byte(password)
	// Try the symmetric passphrase first
	if len(symKeys) != 0 && pwdRaw != nil {
		for _, s := range symKeys {
			key, cipherFunc, err := s.Decrypt(pwdRaw)
			if err == nil {
				return &SymmetricKey{
					Key:  key,
					Algo: getAlgo(cipherFunc),
				}, nil
			}

		}
	}

	return nil, errors.New("password incorrect")
}

// SymmetricKeyPacketWithPassword encrypts the session key with the password and
// returns a binary symmetrically encrypted session key packet.
func (pgp *GopenPGP) SymmetricKeyPacketWithPassword(sessionSplit *SymmetricKey, password string) ([]byte, error) {
	outbuf := &bytes.Buffer{}

	cf := sessionSplit.GetCipherFunc()

	if len(password) <= 0 {
		return nil, errors.New("password can't be empty")
	}

	pwdRaw := []byte(password)

	config := &packet.Config{
		DefaultCipher: cf,
	}

	err := packet.SerializeSymmetricKeyEncryptedReuseKey(outbuf, sessionSplit.Key, pwdRaw, config)
	if err != nil {
		return nil, err
	}
	return outbuf.Bytes(), nil
}

func getSessionSplit(ek *packet.EncryptedKey) (*SymmetricKey, error) {
	if ek == nil {
		return nil, errors.New("can't decrypt key packet")
	}
	algo := constants.AES256
	for k, v := range symKeyAlgos {
		if v == ek.CipherFunc {
			algo = k
			break
		}
	}

	if ek.Key == nil {
		return nil, errors.New("can't decrypt key packet key is nil")
	}

	return &SymmetricKey{
		Key:  ek.Key,
		Algo: algo,
	}, nil
}

func getAlgo(cipher packet.CipherFunction) string {
	algo := constants.AES256
	for k, v := range symKeyAlgos {
		if v == cipher {
			algo = k
			break
		}
	}

	return algo
}
