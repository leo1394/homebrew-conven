class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.0.2.tar.gz"
  sha256 "e73a68750d0c42fa3ddf7f1051f4a486b849395e3327cb49aaa3b97be5a1817e"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.0.2"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "97e1d9196b5ba8608a64876f535035544e58ead727befec77cf67e44f1e6f83c"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "db47259c24ece76d5cb89b7997ca8a27c1efb3da5c2f534ece0ef7ce4ce3d39b"
    sha256 cellar: :any,                 x86_64_linux:  "3dab19b9902920350741691f9b47dc3ac5b506ff5ddb2e57147b9eebd077b40c"
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
    assert_match "conven version 1.0.3 (2026-09-07)", shell_output("#{bin}/conven --version")
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
