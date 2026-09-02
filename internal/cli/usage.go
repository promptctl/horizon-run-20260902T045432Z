package cli

// Usage is the help/usage block. appspec/02-invocation.md states the exact
// wording of usage, help, and parser-warning text is human-facing and is not a
// machine-read contract; the grammar it documents is.
const Usage = `Mackup - Keep your application settings in sync.

Usage:
  mackup [options] list
  mackup [options] show <application>
  mackup [options] backup [<application>]
  mackup [options] restore [<application>]
  mackup [options] link install [<application>]
  mackup [options] link uninstall [<application>]
  mackup [options] link [<application>]
  mackup -h | --help
  mackup --version

Options:
  -h, --help                Show this help message and exit.
      --version             Show the version and exit.
  -f, --force               Answer every confirmation with "Yes".
      --force-no            Answer every confirmation with "No".
  -r, --root                Allow mackup to be run as the superuser.
  -n, --dry-run             Show what would be done without touching any file.
  -v, --verbose             Show fuller progress, including full paths.
  -c, --config-file <path>  Use <path> as the config file instead of ~/.mackup.cfg.

Modes of operation:
  list              List the applications whose settings mackup can sync.
  show              Show the files and folders mackup syncs for one application.
  backup            Copy configuration files from your home into the Mackup folder.
  restore           Copy configuration files from the Mackup folder into your home.
  link install      Move configuration files into the Mackup folder, symlink them back.
  link              Symlink files already in the Mackup folder into your home.
  link uninstall    Replace the symlinks mackup created with real files again.

<application> is an application key as shown by "mackup list", e.g. vim or git.`
