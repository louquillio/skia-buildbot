Storing Expectations on Firestore
=================================

Gold Expectations are essentially a map of (Grouping, Digest) to Label where Grouping is
currently TestName (but could be a combination of TestName + ColorSpace or something
more complex), Digest is the md5 hash of an image's content, and Label is Positive, Negative,
or Untriaged (default).

These Triples are stored in a large map of maps, i.e. map[string]map[string]int. This is
encapsulated by the type Expectations. If a given (Grouping, Digest) doesn't have a label,
it is assumed to be Untriaged.

There is the idea of the MasterExpectations, which is the Expectations belonging to the
git branch "master". Additionally, there can be smaller BranchExpectations that belong
to a ChangeList (CL) and stay separate from the MasterExpectations until the CL lands.

We'd like to be able to do the following:

  - Store and retrieve Expectations (both MasterExpectations and BranchExpectations).
  - Update the Label for a (Grouping, Digest).
  - Keep an audit record of what user updated the Label for a given (Grouping, Digest).
  - Undo a previous change.
  - Support Gerrit CLs and GitHub PullRequests (PRs) - which both have issue IDs of int64

Of note, there will only be one writer to the Firestore (the main skiacorrectness server)
and potentially other readers (e.g. baseline server).

Schema
------

In the spreadsheet metaphor, Firestore Collections are _tables_ and Documents
are the _rows_, with the fields of the Documents being the columns.

Like all other projects, we will use the firestore.NewClient to create a top level
"gold" Collection with a parent Document for this instance (e.g. "skia-prod", "flutter", etc).
Underneath that parent Document, we will create a three Collections:
`expectations`, `triage_records`, and `triage_changes`.

In the `expectations` Collection, we will store many `expectationEntry` Documents with
the following schema:

	Grouping       string    # starting as the TestName
	Digest         string
	Label          int
	Updated        time.Time
	Issue          int64     # 0 for master branch, nonzero for CLs

The `expectationEntry` will have an ID of `[grouping]|[digest]`, allowing updates.

The `triage_records` Collection will have `triageRecords` Documents:

	ID           string    # autogenerated
	UserName     string
	TS           time.Time
	Issue        int64
	Committed    bool      # if writing has completed (e.g. large triage)
	Changes      int       # how many records match in triage_changes Collection

The `triage_changes` Collection will have `triageChanges` Documents:

	RecordID       string # From the triage_records table
	Grouping       string
	Digest         string
	LabelBefore    int
	LabelAfter     int

The vast majority of LabelAfter will be Positive, with some Negatives and a rare
Untriaged (in the case of an undo).

We split the triage data into two tables to account for the fact that bulk triages can sometimes be
across thousands of groups/digests, which would surpass the 10Mb firestore limit per Document.

Indexing
--------
Firebase has pretty generous indexing limits, so we should be fine with the default single-field
indexes and will add any composite indexes as needed (and will update this section).

Usage
-----

To create the MasterExpectations map (at startup), we simply query all `expectationEntry`
Documents with Issue==0 and assemble them together. The implementation will have an Expectations
map in RAM that acts as a write-through cache.

BranchExpectations will have their changed Expectations (essentially their delta from the
MasterExpectations) stored in the `expectations` Collection with nonzero
Issue fields. When the tryjob monitor notes that a CL has landed, it can make a transaction
to change all the Issue fields of the associated Documents in the `expectations` Collection to 0.

Storing the data as above yields for trivial triage log fetching:

	q := client.Collection("triage_records").OrderBy("Updated").Limit(N).Offset(M)
	firestore.IterDocs("", "", q, 3, 5*time.Second, ...)

To undo, we can query the original change by id (from the `triage_records` Collection)
and simply apply the opposite of it, if the current state matches the labelBefore
(otherwise, do nothing, because either it has been changed again or already undone).

Growth Opportunities
-------------------

The design should be open to future changes, for example:

  1. Specifying a maximum age of an expectation. e.g. Forget about positive digests not seen for
    a year, forget about negative digests not seen for 6 months.
  2. Add in the ability to say *why* something was marked negative.

For item #1, the schema could be augmented with a "last seen on" timestamp that is written to
once per day or so in a batch write. Note: to not overly tax the indexes, the last seen
timestamps should all be the same for each batch write.

The schema could be augmented for #2 with additional fields in the `expectations` and
`triage_changes` Collections and some UI support.