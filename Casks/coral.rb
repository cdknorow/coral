cask "coral" do
  version "1.1.3"
  sha256 "a5dc1f5deae8d0eb7a112771339b881b192be4d015ef4e798ff336b8eed171cb"

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
