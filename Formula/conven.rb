class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.0.5.tar.gz"
  sha256 "cd2624a2cbe4087a9d460800170fdfeec2188d86605d2ceaa23da684b65f5638"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.0.5"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "ceddfaec72821e054bd4b43fd56136225f4ee5ab53e02ab462a4d0a5d95d7f39"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "3cc2da4e7429e6f93c46bc07991662534a46c6e22c3f0bea861151f0ae167dc7"
    sha256 cellar: :any,                 x86_64_linux:  "659257cab8db19af0557298f1f8f11a5e8c35186d72ffb6a107181f80ec47d15"
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
    assert_match "conven version 1.0.5 (2026-09-11)", shell_output("#{bin}/conven --version")
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
