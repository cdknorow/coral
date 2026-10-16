cask "coral" do
  version "1.4.63"
  sha256 "1daa9c25708d1cc422638e339ab2e8b9d3ad9b2382c3fc3f352126b9c955e3aa"

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
