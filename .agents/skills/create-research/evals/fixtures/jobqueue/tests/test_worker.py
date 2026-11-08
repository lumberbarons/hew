from unittest import mock

from jobqueue import worker


def test_failed_job_is_requeued_with_attempt_count():
    with mock.patch("jobqueue.worker.queue") as q:
        worker.process({"id": 1}, mock.Mock(side_effect=ValueError))
        q.enqueue.assert_called_once_with({"id": 1, "attempts": 1})


def test_job_is_buried_after_max_retries():
    with mock.patch("jobqueue.worker.queue") as q:
        worker.process({"id": 1, "attempts": 2}, mock.Mock(side_effect=ValueError))
        q.bury.assert_called_once()
