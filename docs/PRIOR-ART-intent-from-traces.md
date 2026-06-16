# Prior art: inferring intent from activity logs, applied to agent tool-use traces

_Compiled 2026-06-16. A literature pass to ground the tool-improvement harness — the
question being "how do we judge whether a tool served the agent's intent," not "how much
did it burn." Five bodies of work, what each assumes, and what transfers._

## The one finding that organizes everything

Every field below was built to reconstruct **latent, unobserved** intent from a thin
signal — clicks, queries, or actions under a formal model. They are all, at heart,
machinery for guessing a "why" that was never written down.

Our setting breaks that assumption, in our favour. The intent is **partly observed**: the
user prompt and the agent's own stated reasoning sit next to every tool call. A clickstream
is a shopper clicking in silence; an agent trace is a shopper narrating why they click.

So the move throughout is the same: **borrow each field's structural machinery, discard its
latent-inference stance.** Replace the weak proxies these methods were forced to use
(edit-distance, query co-occurrence, cost-optimal-plan assumptions) with the stated intent
we already have, and use the action sequence to verify and localize rather than to guess.

This also validates the starting instinct. Counting tool A vs tool B (rg vs snipe) is
exactly the pure-behavioral substitution signal that search-log research abandoned by 2008,
because it ignores the task. The unit of analysis is wrong, not just the metric.

## 1. Search query intent + task segmentation (information retrieval)

**What it offers.** A coarse intent taxonomy and, more importantly, the lesson that
sessions are not tasks.

- Broder's taxonomy (navigational / informational / transactional, SIGIR Forum 2002),
  refined by Rose & Levinson (WWW 2004) and automated by Jansen, Booth & Spink (IP&M 2008).
  Maps to tool use as: locate a specific symbol / understand an area / mutate state.
- Jones & Klinkner (CIKM 2008) showed **any time-based session timeout caps near 70%** at
  finding task boundaries, and that tasks are hierarchical (session ⊃ mission ⊃ goal) and
  interleaved. Spink et al. (IP&M 2006) and Lucchese et al. (WSDM 2011, TOIS 2013) found
  **75–90% of activity is multitasking** — non-adjacent items belong to one task.

**Assumption.** Intent is latent and human-labeled for ground truth; the classifier predicts
it from surface proxies (edit distance, co-occurrence, result overlap).

**Transfer.** High, with a correction. Do **not** segment a trace by time gaps or turns —
that demonstrably misgroups interleaved subtasks ("read config → run test → back to the
same edit"). Use the "same-task-pair" framing: decide whether two non-adjacent calls share
a goal. And replace the weak similarity proxies with the agent's stated reasoning, which is
a strictly stronger signal than anything in this literature.

## 2. Clickstream / session intent modeling (e-commerce)

**What it offers.** Sequence-model architectures and a self-supervised objective.

- Session-as-unit, next-event prediction: GRU4Rec (Hidasi et al., ICLR 2016), SASRec
  (Kang & McAuley, ICDM 2018), BERT4Rec (Sun et al., CIKM 2019). Attention recovers which
  prior actions drive the current one; bidirectional encoding is admissible because we
  analyze completed traces.
- Sheil et al. (SIGIR eCom 2018): learned embeddings of raw events beat hand-crafted
  features — and our events carry far more (args, paths, adjacent text).

**Assumption — and where it does NOT transfer.** This is the purest case of the latent
stance: intent is reverse-engineered from opaque item IDs, and the dominant signal is often
a downstream-derived scalar (Sakar et al. 2019: Google Analytics "PageValues" swamps the
sequence; labels come from a binary purchase event). For agent traces there is no clean
conversion label, and the richest signal is text. Borrow the next-action architecture; do
not import the assumption that intent must be squeezed out of intent-free clicks.

## 3. Goal & plan recognition (symbolic AI)

**What it offers.** A formal theory of inferring a goal from actions, and a design dual.

- Plan recognition as planning: Ramírez & Geffner (IJCAI 2009; probabilistic AAAI 2010) —
  a goal is more probable the more the observed path looks like an efficient route to it.
- Landmark-based goal recognition (Pereira, Oren & Meneguzzi, AIJ 2020): score a goal by
  how many of its **necessary milestones** the trace has hit, weighted by how uniquely those
  milestones point at it. Cheaper and does not require the agent to be optimal.
- Goal Recognition Design (Keren, Gal & Karpas, ICAPS 2014): engineer the environment so
  intent is revealed early (minimize the ambiguous prefix).

**Assumption — the obstacle.** Mature methods need two things agent traces lack natively: a
formal action model (preconditions/effects) and an enumerated candidate-goal set. They also
assume a near-rational agent; LLM agents backtrack and explore.

**Transfer.** Landmark scoring is the best symbolic fit: define milestones **informally from
text** ("ship a feature" ≈ read → edit → test → commit), score progress and uniqueness, no
PDDL required. The latent-space line (Amado et al.) is the precedent for skipping the formal
model — and here the LLM itself supplies both missing artifacts: it generates candidate
goals from the prompt and judges plan plausibility in place of a planner. Goal Recognition
Design is the principled argument for instrumenting tools to declare/reveal intent early
(it formalizes the snipe-self-reporting idea).

## 4. Process mining & conformance checking (BPM)

**What it offers — the most directly applicable frame.** A discipline for exactly this.

- Minimal event log = (case, activity, timestamp). For us: case = session/task, activity =
  tool call, timestamp = call time (van der Aalst, *Process Mining*, 2011; IEEE Manifesto
  2011). Ioannou, Burattin & Weber (CAiSE 2018) already did this for IDE telemetry — and had
  to synthesize a case ID and clean noise exactly as we would.
- **Discovery** (descriptive: what processes actually happen) vs **conformance** (normative:
  does reality match a reference). Heuristic/Inductive/Fuzzy miners discover; token replay
  and alignments (Rozinat 2008; Adriansyah 2012) check fitness and localize *where* a trace
  deviated.
- Four quality dimensions — fitness, precision, generalization, simplicity (Buijs, van Dongen
  & van der Aalst 2012/2014) — with the rule that **fitness alone yields a useless "flower
  model."** That is the formal statement of "a tool was invoked ≠ the tool served its
  purpose."
- Trace clustering (Song, Günther & van der Aalst 2008) for variant analysis; "usage smells"
  (Damevski et al., TSE 2017) for anti-pattern mining.

**Transfer.** This names ferret's gap precisely. ferret today is **discovery only** — it
mines repeated motifs (de facto model). The missing half is **conformance**: replay a
session against a reference (best practice, or the agent's own stated plan) and report
fitness + precision, with alignments pinpointing the failed call. ferret's `Edit!⇝Read` /
`Write!⇝Read⇝Write` targets are textbook usage smells. And the one thing classic process
mining lacks — semantics — is the thing we have: the de jure reference can be derived from
the agent's own words rather than hand-authored.

## 5. LLM agent / tool-use trajectory evaluation (2023–2025)

**What it offers.** Recent, directly-aimed methods for scoring how an agent used tools.

- **TRACE** (arXiv:2510.02837, 2025): reference-free, multi-axis trajectory scoring on
  **efficiency, hallucination, adaptivity** — a near-exact operational form of
  friction-free / effective / recoverable. No gold path needed.
- **τ-bench** (Yao et al., 2024): **pass^k** — probability all k reruns of the same task
  succeed. GPT-4o's pass^8 < 25% in retail: agents are inconsistent, not just wrong.
  Consistency is the measurable core of "delightful."
- **BFCL** (Berkeley): AST-level checking of tool selection + argument correctness, cheap and
  execution-free — "right tool, right args, for the stated goal."
- **Process reward models** + contrastive success/failure mining (ExpeL): attribute friction
  to a specific step, and learn which affordances separate good from bad traces.
- **Coding-agent trace studies** (UCL ICSE 2026, arXiv:2511.00197, the most on-point):
  failed trajectories are **12–82% longer and higher-variance**; coarse file localization is
  rarely the problem (failed runs hit the right file 59–81% of the time) — fine-grained
  reasoning is; **approximate beats exact** (±5 lines predicts success, perfect match is an
  unproductive goal); a top failure mode is **inability to abandon an unproductive loop.**

**Caveat (verification).** AgentRewardBench (2025) shows LLM judges over traces are biased
toward **declaring success** and no single judge wins across categories. Any automated
tool-fit scorer must be validated against human-labeled side-effect / repetition annotations
before it's trusted in a continuous loop. This is the empirical reason to keep dk's judgment
as the validator, not a burn or judge score.

## What this means for the harness (actionable)

1. **Change the unit from call to task.** Segment traces into tasks by shared goal, not by
   time or turn (IR §1). The agent's stated reasoning is the segmentation signal.

2. **Extract a stated-intent sentence per task** (LLM intent extraction, §5). This is the
   substitute for the formal domain model + candidate-goal set that classical recognition
   demands (§3), and the de jure reference that conformance needs (§4).

3. **Give ferret its missing half: conformance.** Keep discovery (the motif mining it already
   does), add replay against a reference + fitness/precision + alignment to localize the
   failed call (§4). "Invoked ≠ served" becomes measurable.

4. **Score tools on the right axes, reference-free.** Efficiency / hallucination / adaptivity
   per task (TRACE), and pass^k consistency per intent-cluster for "delight" (§5). Friction =
   length/variance + unabandoned loops, which matches ferret's own findings.

5. **Score goal progress with landmarks, not plans** (§3): necessary milestones defined from
   text, weighted by uniqueness. Cheap, model-free, tolerant of a backtracking agent.

6. **Instrument tools to reveal intent early** (Goal Recognition Design, §3) — the principled
   case for snipe (and others) emitting internal signal into the logs.

7. **Keep a human in the loop.** LLM judges over traces over-credit success (§5); dk's
   judgment stays the validator.

### The short version

ferret is a process-discovery engine missing its conformance half. The literature says: stop
counting tool substitutions, segment into tasks, read the intent that's already written down,
and replay each task against that intent to see where the tool failed it — scoring on
efficiency, consistency, and milestone-progress, with a human checking the judge.

## Sources

IR / segmentation: Broder SIGIR Forum 2002; Rose & Levinson WWW 2004; Jansen, Booth & Spink
IP&M 2008; Jones & Klinkner CIKM 2008; Spink et al. IP&M 2006; Lucchese et al. WSDM 2011 /
TOIS 2013; Mehrotra & Yilmaz SIGIR 2017; Verberne et al. CHIIR 2023.

Clickstream: Montgomery et al. Marketing Science 2004; Moe JCP 2003; Sakar et al. NCA 2019;
Hidasi et al. ICLR 2016; Kang & McAuley ICDM 2018; Sun et al. CIKM 2019; Sheil et al. SIGIR
eCom 2018.

Goal/plan recognition: Ramírez & Geffner IJCAI 2009 / AAAI 2010; Meneguzzi & Pereira IJCAI
2021; Pereira, Oren & Meneguzzi AIJ 2020; Masters & Sardiña JAIR 2019; Keren, Gal & Karpas
ICAPS 2014; Amado et al. (Goal Recognition in Latent Space).

Process mining: van der Aalst *Process Mining* 2011 / IEEE Manifesto 2011; van der Aalst,
Weijters & Măruşter TKDE 2004; Weijters et al. (HeuristicsMiner) 2006; Leemans et al. Petri
Nets 2013; Günther & van der Aalst BPM 2007; Rozinat & van der Aalst IS 2008; Adriansyah et
al. BPM 2012; Buijs et al. CoopIS 2012; Song, Günther & van der Aalst BPM 2008; Ioannou,
Burattin & Weber CAiSE 2018; Damevski et al. TSE 2017.

LLM agent eval: TRACE arXiv:2510.02837; AgentRewardBench arXiv:2504.08942; τ-bench
arXiv:2406.12045; BFCL (Berkeley, ICML 2025); ToolLLM/ToolBench 2023; process-reward survey
arXiv:2510.08049; Reflexion (Shinn et al. 2023); ExpeL 2024; Majgaonkar et al. ICSE 2026
(arXiv:2511.00197); Bouzenia & Pradel arXiv:2506.18824.
