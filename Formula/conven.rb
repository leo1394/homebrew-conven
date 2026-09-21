class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.0.6.tar.gz"
  sha256 "3a8fa07e5ee186054ae0f0166cb13c54dd6fb8090d4ccf5f7218721d81590346"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.0.6"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "4c755faa6f805436764b930550bf09aaaa407896596abd3918fc85d9f272a216"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "cd26ac0ff8b88ae90381a2402ef25ef35720945ab3eeac0b38adaadb6c2761cf"
    sha256 cellar: :any,                 x86_64_linux:  "747d95ec8e0eba67cc1346d635e3eab1e1c5e9359b19811fdb4e96f4bb1d2da6"
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
    expected_version = build.head? ? "conven version 1.1.0 (2026-09-21)" : "conven version #{version} ("
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
