class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.1.6.tar.gz"
  sha256 "d2e79703aa9147152fc685f6744ffdefb6bf7964e01d1853c0feed8c9c28e036"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.1.6"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "fb2a1c698b04f9a1a205fef63840f596df80f134747ec55da5f654a63950bfa1"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "50f3f6d690746490f63cea3eb05d667a5acb82561240b5b8ad607f59c812140f"
    sha256 cellar: :any,                 x86_64_linux:  "c6db43b4926a62b1c8e019c56f0394b8eb74cbee0081dc6b316aa945073f2738"
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
    expected_version = build.head? ? "conven version 1.1.6 (2026-11-03)" : "conven version #{version} ("
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
