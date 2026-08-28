cask "coral" do
  version "1.0.8"
  sha256 "98e23d894b728249f9aa46ffc30b7649be6426a78843ae0caa4ad2bc41678c57"

  url "https://github.com/cdknorow/coral/releases/download/v#{version}/Coral.v#{version}.dmg",
      verified: "github.com/cdknorow/coral/"
  name "Coral"
  desc "Multi-agent orchestration system for AI coding agents"
  homepage "https://github.com/cdknorow/coral"

  # The tmux backend is the default on macOS. Coral also ships a native PTY
  # backend (--backend pty), but tmux is what an unflagged launch uses.
  depends_on formula: "tmux"
  depends_on macos: :ventura

  app "Coral.app"

  zap trash: "~/.coral"

  caveats <<~EOS
    Coral requires tmux for agent management.
    tmux has been installed as a dependency.

    Launch Coral from your Applications folder or Spotlight.
    The dashboard runs at http://localhost:8420.

    The `coral` and `coral-board` command-line tools are not on your PATH by
    default. To add them:
      #{appdir}/Coral.app/Contents/MacOS/install-cli.sh
  EOS
end
