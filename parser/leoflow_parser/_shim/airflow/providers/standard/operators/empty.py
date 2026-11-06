from airflow._core import BaseOperator


class EmptyOperator(BaseOperator):
    """Airflow's no-op operator. The compiler turns it into a bash task that
    runs ``true`` (#2337)."""
