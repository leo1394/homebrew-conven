class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.0.4.tar.gz"
  sha256 "9c26bdc1a14fce32298446bb77b56df14b5a7998c6f8fcadaab93a95af8da2ad"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.0.4"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "14db82c13d6d536c396209c1a2814b12559b8529ee083c57976ab2d7f062a49b"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "707874b519401b3e6ef0bb36ee4ab24cbe21ee45922a777034711a6c40b36562"
    sha256 cellar: :any,                 x86_64_linux:  "041604c8fa54b122c36ccc57760b538cda7bad3177c18dd3d02873b590bf5d7c"
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
    assert_match "conven version 1.0.4 (2026-09-11)", shell_output("#{bin}/conven --version")
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
