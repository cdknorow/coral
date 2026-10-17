cask "coral" do
  version "1.4.71"
  sha256 "1fe7faa5a41365bb77434ffd9bcdd2732c7dc63fdb793729392214dfcbff91d6"

  url "https://github.com/cdknorow/coral/releases/download/v#{version}/Coral.v#{version}.dmg"
  name "Coral"
  desc "Multi-agent orchestration system for AI coding agents"
  homepage "https://github.com/cdknorow/coral"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: :ventura

  app "Coral.app"

  zap trash: "~/.coral"

  caveats <<~EOS
    Coral bundles its own tmux; nothing else needs to be installed.

    Launch Coral from your Applications folder or Spotlight.
    The dashboard runs at http://localhost:8420.

    The `coral`, `coral-board` and `coral-agent` command-line tools are not on
    your PATH by default. To add them:
      #{appdir}/Coral.app/Contents/MacOS/install-cli.sh
  EOS
end
