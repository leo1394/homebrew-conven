class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.1.0.tar.gz"
  sha256 "d1841a3ed1eb56c86b83f1dbc92dbeca732ae2dd47bcc916b7789b024111d9e2"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.1.0"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "288794a14dcab355a6c5bda8b2369a0710cc4879756ddc229a15969d4f0a108a"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "4fe0264cf5a51b44438c24d7fae11ae14eedf88a7dd38987ac5cb08e00fdfebb"
    sha256 cellar: :any,                 x86_64_linux:  "7ee9406f74956808c8b21346e9c67506cad14e19f25c4caad90486a662846d75"
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
