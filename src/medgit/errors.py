"""Errors for the MedGit clinical object log."""


class MedGitError(Exception):
    """Base class for every MedGit failure."""


class PolicyError(MedGitError):
    """A role or workflow rule rejected the operation."""


class IntegrityError(MedGitError):
    """The object store or hash chain is invalid."""


class RewriteForbidden(MedGitError):
    """Clinical history is append-only. The requested rewrite does not exist."""
