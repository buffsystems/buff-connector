"""A stand-in for the `tqdm` progress bar pysmb imports: the plugin transfers
files silently, so the bar is never shown — and a progress bar is not worth a
dependency crossing into a customer's network."""


class tqdm:  # noqa: N801 — the name pysmb imports
    def __init__(self, *args, **kwargs) -> None:
        pass

    def update(self, n=1) -> None:
        pass

    def close(self) -> None:
        pass

    def __enter__(self):
        return self

    def __exit__(self, *exc) -> None:
        pass
