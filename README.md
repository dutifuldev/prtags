# ghanno

`ghanno` is a curation layer on top of mirrored GitHub data.

The project is meant to sit next to `ghreplica`, not replace it. `ghreplica` stays responsible for mirroring GitHub objects and serving Git-backed change/search truth, while `ghanno` stores human-added structure such as groups, annotations, intent, quality judgments, and later semantic features.

The starting point for this repo is the data model. See [docs/DATA_MODEL.md](docs/DATA_MODEL.md). Field-definition details live in [docs/ANNOTATION_FIELDS.md](docs/ANNOTATION_FIELDS.md).
