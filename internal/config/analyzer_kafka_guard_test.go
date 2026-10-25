package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKafkaConsumerEvidenceWithConfiguredGuard(t *testing.T) {
	repository := t.TempDir()
	cases := []struct {
		file string
		source string
		env string
		property string
		protected bool
	}{
		{"events.go", `package events
func run() {
    if os.Getenv("APP_EVENTS_ENABLED") == "false" { return }
    kafka.NewReader(config)
}`, "APP_EVENTS_ENABLED", "", true},
		{"events.go", `package events
func run() {
    if os.Getenv("OTHER_EVENTS_ENABLED") == "false" { return }
    kafka.NewReader(config)
}`, "APP_EVENTS_ENABLED", "", false},
		{"events.go", `package events
func run() {
    goto consume
    if os.Getenv("APP_EVENTS_ENABLED") == "false" { return }
consume:
    kafka.NewReader(config)
}`, "APP_EVENTS_ENABLED", "", false},
		{"Events.java", `@ConditionalOnProperty(name = "app.events.enabled", havingValue = "true", matchIfMissing = true)
class Events { @KafkaListener void consume() {} }`, "", "app.events.enabled", true},
		{"Events.java", `// @ConditionalOnProperty(name = "app.events.enabled", havingValue = "true", matchIfMissing = true)
class Events { @KafkaListener void consume() {} }`, "", "app.events.enabled", false},
		{"Events.java", `@ConditionalOnProperty(name = "other.events.enabled", havingValue = "true", matchIfMissing = true)
class Events { @KafkaListener void consume() {} }`, "", "app.events.enabled", false},
		{"Events.java", `@ConditionalOnProperty(prefix = "service.kafka.consumers", name = "enabled", havingValue = "true", matchIfMissing = true)
class Events { @KafkaListener void consume() {} }`, "APP_EVENTS_ENABLED", "", false},
		{"Events.java", `@ConditionalOnProperty(name = "app.events.enabled", havingValue = "true", matchIfMissing = true)
void unrelated() {}
class Events { @KafkaListener void consume() {} }`, "", "app.events.enabled", false},
		{"events.ts", `import { Kafka } from "kafkajs";
if (process.env.APP_EVENTS_ENABLED !== "false" || bypass) {
  consumer.subscribe({topic: "events"});
}`, "APP_EVENTS_ENABLED", "", false},
		{"events.ts", `import { Kafka } from "kafkajs";
if (process.env.APP_EVENTS_ENABLED !== "false") {
  consumer.subscribe({topic: "events"});
}`, "APP_EVENTS_ENABLED", "", true},
		{"events.py", `from kafka import KafkaConsumer
if os.getenv("APP_EVENTS_ENABLED", "true").lower() != "false" or bypass:
    KafkaConsumer("events")
`, "APP_EVENTS_ENABLED", "", false},
		{"events.py", `from kafka import KafkaConsumer
if os.getenv("APP_EVENTS_ENABLED", "true").lower() != "false":
    KafkaConsumer("events")
`, "APP_EVENTS_ENABLED", "", true},
	}
	for index, test := range cases {
		path := filepath.Join(repository, test.file)
		if err := os.WriteFile(path, []byte(test.source), 0600); err != nil {
			t.Fatal(err)
		}
		evidence, err := InspectKafkaConsumerEvidenceWithGuard(repository, ".", test.env, test.property)
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		found := false
		for _, item := range evidence {
			if strings.HasPrefix(item.File, test.file+":") {
				found = true
				if item.Protected != test.protected {
					t.Fatalf("case %d: evidence = %#v", index, evidence)
				}
			}
		}
		if !found {
			t.Fatalf("case %d: missing evidence: %#v", index, evidence)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKafkaGuardRangesIgnoreLiteralAndCommentBraces(t *testing.T) {
	java := `@ConditionalOnProperty(name = "app.events.enabled", havingValue = "true", matchIfMissing = true)
class Guarded {
    String brace = "{";
    /* } */
    @KafkaListener void guarded() {}
}
class Unguarded { @KafkaListener void unguarded() {} }`
	evidence := javaKafkaConsumerEvidenceInSourceWithGuard("Events.java", java, "app.events.enabled")
	if len(evidence) != 2 || !evidence[0].Protected || evidence[1].Protected {
		t.Fatalf("Java literal brace evidence = %#v", evidence)
	}

	node := "import { Kafka } from 'kafkajs';\n" +
		"if (process.env.APP_EVENTS_ENABLED !== 'false') {\n" +
		"  console.log('{'); /* } */\n" +
		"  console.log(`{}`);\n" +
		"  consumer.subscribe({topic: 'events'});\n" +
		"}\n" +
		"consumer.run();\n" +
		"console.log('}');\n"
	evidence = nodeKafkaConsumerEvidenceInSourceWithGuard("events.ts", node, "APP_EVENTS_ENABLED")
	if len(evidence) != 2 || !evidence[0].Protected || evidence[1].Protected {
		t.Fatalf("Node literal brace evidence = %#v", evidence)
	}

	interpolated := "import { Kafka } from 'kafkajs';\n" +
		"if (process.env.APP_EVENTS_ENABLED !== 'false') {\n" +
		"  console.log(`${value}`);\n" +
		"  consumer.subscribe({topic: 'events'});\n" +
		"}\n"
	evidence = nodeKafkaConsumerEvidenceInSourceWithGuard("events.ts", interpolated, "APP_EVENTS_ENABLED")
	if len(evidence) != 1 || evidence[0].Protected {
		t.Fatalf("ambiguous template interpolation evidence = %#v", evidence)
	}
}

func TestSpringKafkaGuardDoesNotProtectNestedClass(t *testing.T) {
	source := `@ConditionalOnProperty(name = "app.events.enabled", havingValue = "true", matchIfMissing = true)
class Outer {
    @KafkaListener void guarded() {}
    @Component static class Inner {
        @KafkaListener void independentlyRegistered() {}
    }
}`
	evidence := javaKafkaConsumerEvidenceInSourceWithGuard("Events.java", source, "app.events.enabled")
	if len(evidence) != 2 || !evidence[0].Protected || evidence[1].Protected {
		t.Fatalf("nested Spring consumer evidence = %#v", evidence)
	}
}

func TestGoKafkaPackageInitializersAreUnprotected(t *testing.T) {
	source := []byte(`package events
var direct = kafka.NewReader(config)
var initialized = func() *kafka.Reader {
    if os.Getenv("APP_EVENTS_ENABLED") == "false" { return nil }
    return kafka.NewReader(config)
}()
func guarded() {
    if os.Getenv("APP_EVENTS_ENABLED") == "false" { return }
    kafka.NewReader(config)
}`)
	evidence, err := goKafkaConsumerEvidenceInSourceWithGuard("events.go", source, "APP_EVENTS_ENABLED")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 3 || evidence[0].Protected || evidence[1].Protected || !evidence[2].Protected {
		t.Fatalf("package initializer evidence = %#v", evidence)
	}
}

func TestSpringKafkaGuardDoesNotProtectNestedRecordOrEnum(t *testing.T) {
	source := `@ConditionalOnProperty(name = "app.events.enabled", havingValue = "true", matchIfMissing = true)
class Outer {
    @KafkaListener void guarded() {}
    @Component record IndependentRecord() {
        @KafkaListener void unguardedRecord() {}
    }
    @Component enum IndependentEnum {
        INSTANCE;
        @KafkaListener void unguardedEnum() {}
    }
}`
	evidence := javaKafkaConsumerEvidenceInSourceWithGuard("Events.java", source, "app.events.enabled")
	if len(evidence) != 3 || !evidence[0].Protected || evidence[1].Protected || evidence[2].Protected {
		t.Fatalf("nested Spring record/enum evidence = %#v", evidence)
	}
}

func TestJavascriptKafkaGuardRejectsRegexLiteralBraces(t *testing.T) {
	source := `import { Kafka } from "kafkajs";
if (process.env.APP_EVENTS_ENABLED !== 'false') {
    const re = /[{]/;
}
consumer.run();
const re2 = /[}]/;`
	evidence := nodeKafkaConsumerEvidenceInSourceWithGuard("events.ts", source, "APP_EVENTS_ENABLED")
	if len(evidence) != 1 || evidence[0].Protected {
		t.Fatalf("regex literal brace evidence = %#v", evidence)
	}
}

func TestGoKafkaNamedWrappersRequireGuard(t *testing.T) {
	source := []byte(`package events
func start() {
    reader := &mq.KafkaReader{}
    reader.Init("shared", "events")
    mq.NewKafkaReader(config)
    new(custom.KafkaConsumer)
    KafkaReader{}
    mq.KafkaWriter{}
    var extra mq.KafkaReader
    extra.Init("shared", "events")
    NewKafkaReader(config)
}
func guarded() {
    if os.Getenv("APP_EVENTS_ENABLED") == "false" { return }
    custom.KafkaReader{}
}`)
	evidence, err := goKafkaConsumerEvidenceInSourceWithGuard("events.go", source, "APP_EVENTS_ENABLED")
	if err != nil { t.Fatal(err) }
	if len(evidence) != 7 { t.Fatalf("wrapper evidence = %#v", evidence) }
	for index, item := range evidence {
		if item.Protected != (index == 6) { t.Fatalf("wrapper evidence = %#v", evidence) }
	}
}
