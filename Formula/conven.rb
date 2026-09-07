class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.0.3.tar.gz"
  sha256 "a99a30455a4c06e798c9e9ce5b1422f6467eb01444f8ef295ae5cfc636e4b35f"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.0.3"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "77ca15bfe654f37c31a157418fd37918ce41f161401cebb656a306c126f5c45e"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "eb844f4a9a49fa229d68d7ffd6669ae8548f3db814d33ebee2721a37f38593d3"
    sha256 cellar: :any,                 x86_64_linux:  "6452af16ad75e567c8033840e5f917be7f8afcbd6f0df4951e24e70b27909cbf"
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
