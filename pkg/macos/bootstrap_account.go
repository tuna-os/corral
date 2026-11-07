package macos

import (
	"archive/tar"
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"howett.net/plist"
)

// These are password-equivalent private material; callers must clear the byte
// slices after use and store resumable state only in their private state scope.
type bootstrapAccount struct {
	User       string
	UUID       string
	Record     []byte
	KCPassword []byte `json:"-"`
}

//go:embed bootstrap_install.sh
var bootstrapInstallScript string

//go:embed bootstrap_firstboot.sh
var bootstrapFirstbootScript []byte

var bootstrapFirstbootPlist = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.corral.bootstrap-firstboot</string>
<key>ProgramArguments</key><array><string>/bin/bash</string><string>/Library/Corral/bootstrap-firstboot.sh</string></array>
<key>RunAtLoad</key><true/><key>StartInterval</key><integer>30</integer>
<key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string>
</dict></plist>
`)

const bootstrapIterations = 200000

var bootstrapUserPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
var bootstrapUUIDPattern = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`)

func validBootstrapUser(user string) bool {
	if !bootstrapUserPattern.MatchString(user) {
		return false
	}
	switch user {
	case "root", "daemon", "nobody", "guest", "admin", "administrator", "wheel", "staff", "everyone", "sshd", "corral":
		return false
	}
	return true
}

func validBootstrapPassword(password string) bool {
	return len(password) > 0 && len(password) <= 1024 && utf8.ValidString(password) && !strings.ContainsAny(password, "\x00\r\n")
}

type bootstrapShadow struct {
	Hash struct {
		Entropy    []byte `plist:"entropy"`
		Iterations int    `plist:"iterations"`
		Salt       []byte `plist:"salt"`
	} `plist:"SALTED-SHA512-PBKDF2"`
}

type bootstrapUserRecord struct {
	Name                    []string `plist:"name"`
	UID                     []string `plist:"uid"`
	GID                     []string `plist:"gid"`
	Home                    []string `plist:"home"`
	RealName                []string `plist:"realname"`
	Shell                   []string `plist:"shell"`
	GeneratedUID            []string `plist:"generateduid"`
	Password                []string `plist:"passwd"`
	AuthenticationAuthority []string `plist:"authentication_authority"`
	ShadowHashData          [][]byte `plist:"ShadowHashData"`
	WritersHint             []string `plist:"_writers_hint"`
	WritersJPEGPhoto        []string `plist:"_writers_jpegphoto"`
	WritersPassword         []string `plist:"_writers_passwd"`
	WritersPicture          []string `plist:"_writers_picture"`
	WritersRealName         []string `plist:"_writers_realname"`
	WritersUserCertificate  []string `plist:"_writers_UserCertificate"`
}

func newBootstrapAccount(user, password string, automaticLogin bool) (bootstrapAccount, error) {
	if !validBootstrapUser(user) {
		return bootstrapAccount{}, errors.New("bootstrap requires a non-reserved lowercase local username of 1–31 ASCII characters")
	}
	if !validBootstrapPassword(password) {
		return bootstrapAccount{}, errors.New("bootstrap password must be valid UTF-8, 1–1024 bytes, with no NUL or line breaks")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return bootstrapAccount{}, fmt.Errorf("generate account identity: %w", err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	uuid := fmt.Sprintf("%X-%X-%X-%X-%X", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	var shadow bootstrapShadow
	shadow.Hash.Salt = make([]byte, 32)
	if _, err := rand.Read(shadow.Hash.Salt); err != nil {
		return bootstrapAccount{}, fmt.Errorf("generate credential salt: %w", err)
	}
	shadow.Hash.Iterations = bootstrapIterations
	entropy, err := pbkdf2.Key(sha512.New, password, shadow.Hash.Salt, shadow.Hash.Iterations, 128)
	if err != nil {
		return bootstrapAccount{}, errors.New("derive local account credential")
	}
	shadow.Hash.Entropy = entropy
	defer clear(entropy)
	shadowData, err := plist.Marshal(shadow, plist.BinaryFormat)
	if err != nil {
		return bootstrapAccount{}, errors.New("encode local account credential")
	}
	defer clear(shadowData)
	name := []string{user}
	record := bootstrapUserRecord{
		Name: name, UID: []string{"501"}, GID: []string{"20"}, Home: []string{"/Users/" + user},
		RealName: name, Shell: []string{"/bin/zsh"}, GeneratedUID: []string{uuid}, Password: []string{"********"},
		AuthenticationAuthority: []string{";ShadowHash;HASHLIST:<SALTED-SHA512-PBKDF2>"}, ShadowHashData: [][]byte{shadowData},
		WritersHint: name, WritersJPEGPhoto: name, WritersPassword: name, WritersPicture: name,
		WritersRealName: name, WritersUserCertificate: name,
	}
	body, err := plist.Marshal(record, plist.XMLFormat)
	if err != nil {
		return bootstrapAccount{}, errors.New("encode local account record")
	}
	account := bootstrapAccount{User: user, UUID: uuid, Record: body}
	if automaticLogin {
		account.KCPassword = automaticLoginPassword(password)
	}
	return account, nil
}

func decodeBootstrapRecord(account bootstrapAccount) (bootstrapShadow, bool) {
	var record bootstrapUserRecord
	var shadow bootstrapShadow
	if !validBootstrapUser(account.User) || !bootstrapUUIDPattern.MatchString(account.UUID) || len(account.Record) > 65536 {
		return shadow, false
	}
	if _, err := plist.Unmarshal(account.Record, &record); err != nil {
		return shadow, false
	}
	one := func(values []string, expected string) bool { return len(values) == 1 && values[0] == expected }
	if !one(record.Name, account.User) || !one(record.UID, "501") || !one(record.GID, "20") ||
		!one(record.Home, "/Users/"+account.User) || !one(record.GeneratedUID, account.UUID) ||
		!one(record.Password, "********") || !one(record.Shell, "/bin/zsh") ||
		!one(record.AuthenticationAuthority, ";ShadowHash;HASHLIST:<SALTED-SHA512-PBKDF2>") || len(record.ShadowHashData) != 1 {
		return shadow, false
	}
	defer clear(record.ShadowHashData[0])
	if _, err := plist.Unmarshal(record.ShadowHashData[0], &shadow); err != nil {
		return shadow, false
	}
	if len(shadow.Hash.Salt) != 32 || len(shadow.Hash.Entropy) != 128 || shadow.Hash.Iterations < 30000 || shadow.Hash.Iterations > 1000000 {
		clear(shadow.Hash.Entropy)
		return bootstrapShadow{}, false
	}
	return shadow, true
}

func bootstrapPasswordMatches(account bootstrapAccount, password string) bool {
	if !validBootstrapPassword(password) {
		return false
	}
	shadow, ok := decodeBootstrapRecord(account)
	if !ok {
		return false
	}
	defer clear(shadow.Hash.Entropy)
	derived, err := pbkdf2.Key(sha512.New, password, shadow.Hash.Salt, shadow.Hash.Iterations, 128)
	if err != nil {
		return false
	}
	defer clear(derived)
	if subtle.ConstantTimeCompare(derived, shadow.Hash.Entropy) != 1 {
		return false
	}
	if len(account.KCPassword) != 0 {
		expected := automaticLoginPassword(password)
		defer clear(expected)
		return subtle.ConstantTimeCompare(expected, account.KCPassword) == 1
	}
	return true
}

func writeBootstrapArchive(w io.Writer, account bootstrapAccount, publicKey []byte) error {
	shadow, ok := decodeBootstrapRecord(account)
	clear(shadow.Hash.Entropy)
	if !ok || (len(account.KCPassword) != 0 && (len(account.KCPassword) > 1032 || len(account.KCPassword)%12 != 0)) {
		return errors.New("invalid private bootstrap account state")
	}
	if len(publicKey) > 16384 {
		return errors.New("bootstrap requires one public SSH key")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey(publicKey)
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("bootstrap requires one unadorned public SSH key")
	}
	// Re-encoding drops caller-supplied comments and excludes authorized_keys
	// options or additional lines from the installation payload.
	authorized := ssh.MarshalAuthorizedKey(key)
	tw := tar.NewWriter(w)
	members := [...]struct {
		name string
		mode int64
		body []byte
	}{
		{"user.plist", 0600, account.Record},
		{"authorized_keys", 0600, authorized},
		{"bootstrap-firstboot.sh", 0700, bootstrapFirstbootScript},
		{"bootstrap-firstboot.plist", 0644, bootstrapFirstbootPlist},
		{"kcpassword", 0600, account.KCPassword},
	}
	count := len(members)
	if len(account.KCPassword) == 0 {
		count--
	}
	for _, member := range members[:count] {
		if err := tw.WriteHeader(&tar.Header{Name: member.name, Mode: member.mode, Size: int64(len(member.body)), Typeflag: tar.TypeReg, Uid: 0, Gid: 0, Format: tar.FormatUSTAR}); err != nil {
			return fmt.Errorf("write bootstrap archive header: %w", err)
		}
		if _, err := tw.Write(member.body); err != nil {
			return fmt.Errorf("write bootstrap archive member: %w", err)
		}
	}
	return tw.Close()
}
