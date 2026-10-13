class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.1.3.tar.gz"
  sha256 "c53c9a0c589fecbe261f332a7684c83368ac888fcb413f595ff58535f99aecdc"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.1.3"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "4f615bc7e86eb5c3dd0323ddac90e335fa30c9f71827a8dcb22a9b8fd97a9db7"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "59c50936d9f546577ea3b7968fb0e9f4887fac92351b6fb112bb18d0ac801c7c"
    sha256 cellar: :any,                 x86_64_linux:  "9e844609213ee98f3ab692bcff591efa455fb1c8e2fd95c1fdf4267c542dde0c"
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
    expected_version = build.head? ? "conven version 1.1.3 (2026-10-13)" : "conven version #{version} ("
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
