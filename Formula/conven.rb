class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.1.4.tar.gz"
  sha256 "4af13d27e01a99d4bbfa9548c52ed1926d5746f4ce1c1d7abf319422bbec04de"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.1.4"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "90d5ccc354829adb1b913865f609c53400945f2a7bb4715217b1d64aa158af4e"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "9756232ded5884b928ce55e8d62cd708af7756cf347dd2cc8b14480273ef310b"
    sha256 cellar: :any,                 x86_64_linux:  "f5e6393c84037747feb77017525427788b933cea3c2b8114f5d34edce6a71591"
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
