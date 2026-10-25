class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.1.5.tar.gz"
  sha256 "9b354e16d38accf3714f7d6b7e1cc76ecc31280a6052c3560d980d26d4129a27"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.1.5"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "de20cc0a3d309830aa70ce3c334a0fe29d55e35ecea4f62962224e5be4ecf0a5"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "b657e82ac52005edebba7e5e4a6f7e58af2291ac86a8486a67831b3617dbadad"
    sha256 cellar: :any,                 x86_64_linux:  "0731e1146862a4d1c8c3b6b9afbe47ca7c10919ef549c5c50b3d43ec77f5675d"
  end

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/conven"
    generate_completions_from_executable(bin/"conven", "__completion")
    man1.install "docs/conven.1" if build.head? || version >= "0.2.4"
  end

  test do
    ENV["HOME"] = testpath.to_s
    ENV["LC_ALL"] = "en_US.UTF-8"
    expected_version = build.head? ? "conven version 1.1.5 (2026-10-25)" : "conven version #{version} ("
    assert_match expected_version, shell_output("#{bin}/conven --version")
    assert_predicate bin/"conven", :executable?
    assert_path_exists man1/"conven.1"
    assert_path_exists bash_completion/"conven"
    assert_path_exists zsh_completion/"_conven"
    assert_path_exists fish_completion/"conven.fish"

    workspace = testpath/"workspace"
    workspace.mkpath
    system bin/"conven", "-C", workspace, "init"
    assert_path_exists workspace/".conven/conven.yaml"
    system bin/"conven", "-C", workspace, "workspace", "--validate"
  end
end
