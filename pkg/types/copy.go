package types

// CopyOptions describes exactly one local/guest transfer, not a guest-to-guest
// copy. FromGuest selects download; false selects upload. Paths are literal,
// never patterns or remote shell commands.
type CopyOptions struct {
	LocalPath string
	GuestPath string
	FromGuest bool
	Recursive bool
	User      string
	Identity  string
}
