package apps

// TUIFlagName, TUIFlagShorthand and TUIFlagUsage describe the root command's
// --tui/-t flag. They are preserved from the pre-cobra github.com/urfave/cli/v3
// CLI surface (flag name and shorthand are user-facing and must not change);
// the root command registers them directly as a cobra/pflag bool flag.
const (
	TUIFlagName      = "tui"
	TUIFlagShorthand = "t"
	TUIFlagUsage     = "Start terminal UI"
)
