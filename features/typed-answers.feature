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

  # @test:TestEveryToolSchemaIsValidJSONSchemaForStrictClients
  Scenario: A strict MCP client such as Claude Code accepts the tool list
    Given typryx publishes tools/list over MCP
    When a tool has no required arguments
    Then its schema names an empty array, never a JSON null
    And every name a tool's schema requires is one of its own properties

  # @test:TestConnectPrintsReadyToPasteConfiguration
  Scenario: A customer gets the configuration for their client from one command
    Given a customer picked a client: Claude Code, tokenfuse, or plain curl
    When they run typryx connect for that client
    Then they get a ready-to-paste command and configuration for it
    And no real key is ever printed, only a placeholder and where to set it

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

  # @test:TestTheProbabilityComesFromTheLabelTokensLogprobs
  Scenario: A local model answers with a probability read from its own token probabilities
    Given a local OpenAI-compatible model server with no data leaving the machine
    And a question whose options are relabelled as single letters
    When the server reports its own token probabilities for those letters
    Then the answer's probability for each option comes from that model's own numbers
    And nothing about the model's own prose is ever trusted

  # @test:TestLowLabelMassIsUnansweredNotRenormalized
  Scenario: A model that wants to answer something else is unanswered, not forced into an option
    Given a local model whose top tokens mostly do not match any lettered option
    When it is asked a question
    Then the result is unanswered, with a reason
    And the little probability mass that did land on an option is never stretched to sum to one anyway

  # @test:TestAnOperatorCanSeeWhetherAModelsProbabilitiesCanBeTrusted
  Scenario: An operator can see whether a model's probabilities can be trusted
    Given a template, backend and model with enough recorded outcomes
    When the operator runs typryx calibration
    Then they see its accuracy, its confidence, and a calibration verdict
    And nothing about it is buried where only a developer would look

  # @test:TestAnOverconfidentSourceIsFlagged
  Scenario: A model that says 99 percent and is right far less often is flagged
    Given a template, backend and model that states 99 percent confidence
    And its actual hit rate is far below that
    When the operator runs typryx calibration with a calibration bound
    Then that group's verdict is drift

  # @test:TestTwoModelsAreNeverScoredAsOne
  Scenario: Two models are never scored as one
    Given the same template answered by two different models
    And their calibration is opposite
    When calibration is computed
    Then each model gets its own row, never averaged together

  # @test:TestTooFewTruthsGiveNoVerdict
  Scenario: Too few truths give no verdict
    Given a template, backend and model with fewer recorded outcomes than the minimum
    When calibration is computed
    Then that group's verdict is insufficient
    And no calibration bound is judged against it

  # @test:TestTheRequestCarriesTheStateAsAnObjectAndOneQuestion
  Scenario: Jev answers through the same governed door as every other backend
    Given a template that names two fields of the state it may send
    And Jev is the configured backend
    When it is asked with a state holding more fields than the template names
    Then Jev receives only the already-governed egress, never the raw state
    And its answer is mapped into the same probability shape every backend gives

  # @test:TestARetryNeverCrossesTheDeadline
  Scenario: A rate-limited Jev call is retried, briefly, and never past the caller's deadline
    Given Jev responds that it is rate limited or overloaded
    When a question is asked with a short deadline
    Then Jev retries briefly, with a backoff between attempts
    But it never waits past the caller's own deadline for one more attempt

  # @test:TestTheRecordedModelIsTheServedVersion
  Scenario: The Jev version that answered is what calibration scores
    Given Jev's response names the concrete model version that actually answered
    When the answer is recorded
    Then the recorded model is the version Jev served, not the name that was configured
    And calibration groups by that served version, never by the configured name alone
