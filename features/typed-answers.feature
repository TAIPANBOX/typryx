Feature: Typed answers, as an option a customer adds to the stack

  @decided 2026-09-25: typed-decision models such as Jev are offered to a
  customer as an optional service beside the stack, never as part of its core.
  A customer who does not add it runs exactly the stack they ran before. One
  who does gets a service that asks typed questions (a choice, a score, a yes
  or no) and answers each with a probability, reachable over HTTP and over MCP,
  with the input it sends out, the money it spends and the answers it gives all
  governed and recorded. The model behind it is a backend that can be swapped;
  the question templates are the contract.

  # @test:TestAChoiceQuestionIsAnsweredWithAProbabilityForEveryOption
  Scenario: A question is answered with a probability, not a sentence
    Given a template asking which of three classes a request belongs to
    When an agent asks it about a request
    Then the answer names one class
    And carries a probability for every class, summing to one
    And names the backend and the model version that produced it

  # @test:TestOnlyTheFieldsATemplateNamesReachTheBackend
  Scenario: Only what the question needs leaves the box
    Given a template that names two fields of the state it may send
    When it is asked with a state holding five fields
    Then the backend receives those two fields and nothing else
    And the answer says how many fields were held back

  # @test:TestAFailingBackendGivesUnansweredAndNeverAGuess
  Scenario: No answer is better than an invented one
    Given a backend that fails, times out, or returns no probabilities
    When a question is asked
    Then the service answers "unanswered" with the reason
    And no probability is returned at all

  # @test:TestTheServiceRefusesToStartWithoutANamedBackend
  Scenario: A paid backend is always somebody's explicit choice
    Given no backend is configured
    When the service is started
    Then it refuses to start and says which setting is missing

  # @test:TestTheHourlyCapRefusesTheCallAfterTheLimit
  Scenario: Spend is capped before anyone asks for a cap
    Given the default hourly limit on calls
    When one more question than the limit is asked in an hour
    Then that question is refused as over the cap, before any backend is called

  # @test:TestEveryAnswerAndRefusalIsRecordedWithoutTheState
  Scenario: Every answer is on the record, and the input is not
    Given the journal is switched on
    When a question is answered, and another is refused
    Then each leaves one event naming the agent, the template and its version
    And the record holds a hash of what was sent, never the state itself

  # @test:TestAnEventWithNoAgentIsSkippedAndCounted
  Scenario: An answer nobody can be named for is counted, not attributed
    Given a credential that names no agent
    When it asks a question
    Then the question is answered
    And no event is written under an invented agent
    And the skip is counted where the operator can see it

  # @test:TestIdentityComesFromTheCredentialNeverFromAHeader
  Scenario: Who is asking comes from the credential
    Given a credential bound to one agent
    When a request also carries a header claiming to be another agent
    Then the answer and its record name the agent the credential belongs to

  # @test:TestAWideBindWithNoCredentialRefusesToStart
  Scenario: The service is never an open door on a network
    Given the service is told to listen beyond loopback
    And no client credential is configured
    When it is started
    Then it refuses to start unless the operator said so explicitly

  # @test:TestAnOutcomeIsScoredAgainstTheVersionItWasAskedUnder
  Scenario: A later truth is matched to the question actually asked
    Given an answer given under one version of a template
    And the template has since changed
    When the true outcome for that answer arrives
    Then it is recorded against the version the answer was given under

  # @test:TestAFreeFormQuestionIsRefusedUnlessSwitchedOn
  Scenario: Questions come from templates unless the operator opens it up
    Given free-form questions are not switched on
    When a caller sends a question that is not a template
    Then it is refused and nothing leaves the box

  # @test:TestTheMCPToolAnswersTheSameAsTheHTTPRoute
  Scenario: An agent can reach it as an MCP tool
    Given an MCP client with a valid credential
    When it lists the tools and calls the ask tool with a template and a state
    Then it gets the same typed answer the HTTP route would give

  # @test:TestTheManifestMatchesWhatTheBinaryReads
  Scenario: What the service declares about itself is true
    Given the component manifest
    When the binary is built and started
    Then every variable it reads is declared, and every declared one is read

  # @test:TestTheAnswerIsDerivedFromTheProbabilitiesNotTakenFromTheBackend
  Scenario: The answer comes from the probabilities, not from what the backend claims
    Given a backend that returns a valid probability distribution
    And the same backend also claims a different answer outright
    When a question is asked
    Then the answer served is the one the probabilities actually point to

  # @test:TestAnAnswerWrittenAfterATornLineSurvivesTheNextRestart
  Scenario: An answer written after a crash is still there after the next restart
    Given a ledger whose last line was cut short by a crash
    When the service starts again and answers a new question
    Then the earlier, complete answer is still on record
    And the new answer is written cleanly, not merged with the wreckage

  # @test:TestASecondOutcomeForTheSameAnswerIsRefused
  Scenario: A truth is counted once
    Given an answer that already has a recorded outcome
    When another outcome arrives for that same answer
    Then it is refused, even after the service has restarted
