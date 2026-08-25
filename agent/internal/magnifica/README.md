# magnifica — Magnifica Humanitas launch-banner quotes

This package supplies the curated *Magnifica Humanitas* quote corpus used by the
`machtiani` launch banner when `--magnifica-humanitas` is enabled.  A quote is
selected deterministically for each session: the session ID is whitespace-trimmed,
hashed with SHA-256, and indexed with
`binary.BigEndian.Uint64(digest[:8]) % uint64(len(corpus))`.  The selected quote
is persisted in session state as `magnifica_humanitas`.  Quotes are intentionally
short enough to work in mobile email footers.

## Data format

[`assets/magnifica_humanitas.jsonl`](assets/magnifica_humanitas.jsonl) is embedded
by `go:embed`.  It is JSON Lines: one JSON object per line, with exactly these
three fields:

```json
{"paragraph": 6, "line": 113, "quote": "What direction should we choose as a people and as a human community?"}
```

The `paragraph` and `line` fields record the source location; `quote` is the
stored display text.

## Editorial policy

Each selection must stand on its own, be at most 130 characters, and have broad
appeal to Catholic and non-Catholic readers.

The text is kept **as the Pope wrote it**: every stored word and punctuation mark
comes from the source.  An omission is shown by the encyclical's own ellipsis,
`…` (U+2026): a leading ellipsis drops an opener, an interior ellipsis deletes
words, and a trailing ellipsis truncates the source.  A cut never receives new
punctuation, and capitalization is source-exact (including a lowercase word after
a leading `…`).

Two mirror markdown artifacts are normalized only for storage and comparison:
italic asterisks are removed (paragraph 118's *despite* and *through*), and link
markup is reduced to its displayed title (the paragraph 245 footnote title).
Neither normalization changes the Pope's words or punctuation.

## Corpus

The 59 entries below are ordered by paragraph, then source line and quote.  Length
is the Unicode character count of the exact stored quote.

| Paragraph | Line | Length | Quote |
| ---: | ---: | ---: | --- |
| 1 | 101 | 79 | Yet every era also runs the risk of creating an inhumane and more unjust world. |
| 4 | 109 | 84 | Technology should not be considered, in itself, as a force antagonistic to humanity. |
| 6 | 113 | 115 | If we focus only on contingencies, we risk letting the succession of emergencies dictate the direction of our path. |
| 6 | 113 | 69 | What direction should we choose as a people and as a human community? |
| 9 | 121 | 121 | In the abstract, technology in and of itself is not a solution to humanity’s problems, just as it is not inherently evil. |
| 12 | 129 | 87 | …true fulfilment is not achieved by eliminating weakness but through harmonious growth… |
| 13 | 131 | 100 | Thirdly, building a world in which everyone can flourish requires shared responsibility and courage. |
| 15 | 137 | 54 | …ours is the pressing duty to remain profoundly human… |
| 50 | 235 | 82 | Human dignity does not depend on a person’s abilities, wealth or position in life… |
| 51 | 239 | 79 | The value of persons, however, does not depend on what they achieve or produce. |
| 52 | 241 | 72 | When we speak of dignity, we do not always use the word in the same way. |
| 52 | 241 | 100 | …we refer to moral dignity, namely the way in which a person directs his or her choices and actions. |
| 53 | 243 | 105 | The fundamental dignity of each person… is neither acquired nor earned, nor does it need to be justified. |
| 55 | 249 | 96 | Human rights are inviolable, since they are “inherent in the human person and in human dignity.” |
| 59 | 261 | 86 | We can describe it as the social expression of the dignity recognized in every person. |
| 80 | 311 | 102 | In this day and age, social justice must… grapple with the environment shaped by digital technologies. |
| 83 | 319 | 79 | For individuals as well as for nations, development is both a duty and a right. |
| 93 | 349 | 55 | More power does not necessarily imply something better. |
| 99 | 363 | 125 | Even when these tools are described as capable of “learning,” their way of doing so is different from that of a human person. |
| 100 | 367 | 93 | When words are simulated, they do not build genuine relationships, but only their appearance. |
| 109 | 387 | 87 | The principles of Social Doctrine offer a framework for understanding this new reality. |
| 109 | 387 | 116 | To speak of solidarity obliges us to recognize the hidden, often exploited workers, who sustain algorithmic systems. |
| 110 | 389 | 89 | To disarm does not mean rejecting technology, but preventing it from dominating humanity. |
| 111 | 391 | 90 | …technological innovation can represent human participation in the divine act of creation. |
| 114 | 399 | 79 | The ability to care for one another is a fundamental dimension of our humanity… |
| 114 | 399 | 109 | The quality of a civilization is measured not by the power of its means, but by the care it is able to offer… |
| 118 | 411 | 98 | And yet we must remember that humanity flourishes not despite limitations, but often through them. |
| 120 | 415 | 130 | Even when limitations are experienced as inner suffering, human wisdom teaches us not to deny or suppress it, but to integrate it. |
| 120 | 415 | 94 | To eliminate suffering entirely would mean, in the end, extinguishing love and desire as well. |
| 123 | 421 | 100 | Moral progress almost always unfolds through a long and demanding journey, often marked by setbacks. |
| 125 | 425 | 128 | …goodness does not advance automatically, but requires the perseverance, memory and interior conversion necessary to begin anew… |
| 126 | 427 | 86 | …humanity — in all its grandeur and woundedness — must never be replaced or surpassed. |
| 131 | 447 | 81 | When languages and tools change, so do everyday actions and social relationships. |
| 133 | 455 | 127 | A society is noble and decent, not least for its support of the pursuit of truth and its adherence to the most basic of truths. |
| 134 | 457 | 86 | Indifference to the truth leads, slowly but surely, to a descent into totalitarianism. |
| 134 | 457 | 118 | The search for truth is an essential element of democracy, which is itself a means of contributing to the common good. |
| 149 | 499 | 86 | …work is not simply an instrument; it expresses and enhances the dignity of our lives. |
| 156 | 515 | 76 | …innovation can serve as an ally of safer, more creative and dignified work… |
| 160 | 525 | 122 | Finance for its own sake is fundamentally different from finance aimed at the development, creation and evolution of work. |
| 161 | 527 | 113 | Today, justice requires access to the benefits of innovation, including care, knowledge, tools and opportunities. |
| 165 | 537 | 36 | The family is a primary social good. |
| 167 | 541 | 61 | For young people, job insecurity is particularly devastating. |
| 171 | 553 | 92 | …freedom in the digital age is not merely a matter of interiority but also a public concern. |
| 175 | 563 | 110 | Human trafficking must be recognized as a contemporary form of slavery and a grave violation of human dignity. |
| 177 | 567 | 91 | What we have learned must be translated into discernment and responsibility in the present. |
| 204 | 641 | 66 | We live at a time of significant spiritual and cultural blindness. |
| 210 | 655 | 114 | We do not consider the present as a predetermined fate, but an opportunity for personal and collective conversion. |
| 214 | 667 | 104 | Peace begins with each one of us: in the way we look at others, listen to others and speak about others. |
| 215 | 671 | 96 | All of us, at every level, can contribute to building the foundation of peace, which is justice. |
| 216 | 675 | 129 | Painful events require both history and memory, the former to recount the facts, the latter to bear witness to lived experiences. |
| 218 | 681 | 57 | Authentic realism does not give up on changing the world… |
| 222 | 691 | 81 | Those who make history are the peacemakers, not those who sow seeds of suffering. |
| 224 | 697 | 128 | In international relations, dialogue is an irreplaceable diplomatic tool for preventing conflicts and rebuilding bonds of trust. |
| 233 | 721 | 114 | Even when machines excel in efficiency, a human face that asks to be gazed upon remains the center of our history. |
| 236 | 731 | 106 | We must be faithful to the truth, invest in education, cultivate relationships and love justice and peace. |
| 238 | 735 | 53 | Let us invest in education, beginning with ourselves! |
| 239 | 737 | 67 | …the human heart retains an irrevocable need for genuine closeness… |
| 240 | 739 | 30 | Let us love justice and peace! |
| 245 | 887 | 21 | Quo vadis, humanitas? |

## Verification

Verification parses every JSONL row, requires exactly the three documented
fields, rejects duplicate quote strings, and enforces the 130-character limit.
For source fidelity, it splits each quote at `…`; every non-empty fragment must
occur as a contiguous substring in the normalized mirror, at strictly increasing
positions.  This permits a recorded omission without treating words on either
side as a newly written sentence.

## Provenance

Paragraphs 6, 52, 109, 114, 120, and 134 each contribute two selections.  The
paragraph 245 entry, “Quo vadis, humanitas?”, is borrowed from the title of an
International Theological Commission document cited in the encyclical's
footnotes; it is not encyclical prose.  The two markdown-normalization cases are
the italic asterisks in paragraph 118 and the footnote link markup for paragraph
245 described above.
