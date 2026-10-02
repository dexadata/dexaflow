from airflow._core import BaseOperator


class BashOperator(BaseOperator):
    """Name carries 'Bash' -> Dexaflow 'bash'; reads .bash_command."""
