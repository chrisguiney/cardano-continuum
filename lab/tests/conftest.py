def pytest_addoption(parser):
    parser.addoption("--guide-real", action="store_true", help="run the guide notebook on real data (GUIDE_CHAIN, GUIDE_EXPORT)")
