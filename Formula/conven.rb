class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.1.1.tar.gz"
  sha256 "0613da3d2eaceb938f50aa0cf3587795d15049f1db2f17181d3b50f511bb8096"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.1.1"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "7a6dc8f637949a498e92c4438619a425c1058a53440730755f5567c8247bb75f"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "17ebc316005303549e064865eb6dad63e0df5b846444cea3e5c174a77d74db31"
    sha256 cellar: :any,                 x86_64_linux:  "33156c84693cdef90857ed63246549d5169dce3a26ffad944d8e1b250dffae34"
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
    expected_version = build.head? ? "conven version 1.1.1 (2026-09-25)" : "conven version #{version} ("
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
